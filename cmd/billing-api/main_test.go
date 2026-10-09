package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// TestHTTPShutdownDrainsRequests starts a real request, then requests shutdown.
// The server must wait for that request's response before returning to the
// resource owner, so a deferred database close cannot interrupt its commit.
func TestHTTPShutdownDrainsRequests(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	address, cancel, stopped := startHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
			w.WriteHeader(http.StatusAccepted)
		case <-r.Context().Done():
			t.Error("Shutdown canceled an admitted request before its deadline")
		}
	}), time.Second)
	response := make(chan int, 1)
	go func() {
		client := &http.Client{Timeout: 2 * time.Second}
		result, err := client.Get(address)
		if err != nil {
			response <- 0
			return
		}
		defer result.Body.Close()
		response <- result.StatusCode
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Request never entered the handler")
	}
	cancel()
	select {
	case err := <-stopped:
		t.Fatalf("Server returned with a request still active: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	unblock()
	if status := <-response; status != http.StatusAccepted {
		t.Errorf("Drained response = %d, want 202", status)
	}
	if err := <-stopped; err != nil {
		t.Errorf("Graceful shutdown: %v", err)
	}
}

// TestHTTPShutdownDeadline holds a handler past the drain deadline. Shutdown
// must close its socket, cancel its request context, and report the timeout so
// database operations can release their connections rather than run forever.
func TestHTTPShutdownDeadline(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	address, cancel, stopped := startHTTPServer(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}), 30*time.Millisecond)
	go func() {
		client := &http.Client{Timeout: time.Second}
		if response, err := client.Get(address); err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Request never entered the handler")
	}
	cancel()
	if err := <-stopped; !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown error = %v, want deadline exceeded", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Forced shutdown did not cancel the request")
	}
}

// TestHTTPServeFailure closes the listener before serving. The actual serve
// error must reach the owner instead of being mistaken for a normal shutdown.
func TestHTTPServeFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Open test listener: %v", err)
	}
	listener.Close()
	if err := serveUntilStopped(context.Background(), &http.Server{}, listener, time.Second); err == nil {
		t.Fatal("Closed listener was treated as graceful shutdown")
	}
}

// startHTTPServer runs the production lifecycle on an owned loopback listener.
// Cleanup cancels its lifetime and closes sockets even if an assertion fails.
func startHTTPServer(t *testing.T, handler http.Handler, timeout time.Duration) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Open test listener: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	server := &http.Server{Handler: handler}
	t.Cleanup(func() { cancel(); server.Close() })
	stopped := make(chan error, 1)
	go func() { stopped <- serveUntilStopped(ctx, server, listener, timeout) }()
	return "http://" + listener.Addr().String(), cancel, stopped
}
