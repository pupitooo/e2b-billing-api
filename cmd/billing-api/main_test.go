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

// serveUntilStopped drains admitted requests, cancels them after its deadline,
// and returns listener failures to the resource owner.
func TestServeUntilStopped(t *testing.T) {
	t.Run("httpshutdown drains requests", func(t *testing.T) {
		tt := struct {
			shutdownTimeout    time.Duration
			handlerStatus      int
			wantResponseStatus int
			wantShutdownError  error
		}{
			shutdownTimeout:    time.Second,
			handlerStatus:      http.StatusAccepted,
			wantResponseStatus: http.StatusAccepted,
			wantShutdownError:  nil,
		}

		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		t.Cleanup(unblock)
		address, cancel, stopped := startHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			select {
			case <-release:
				w.WriteHeader(tt.handlerStatus)
			case <-r.Context().Done():
				t.Error("Shutdown canceled an admitted request before its deadline")
			}
		}), tt.shutdownTimeout)
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
		if status := <-response; status != tt.wantResponseStatus {
			t.Errorf("Drained response = %d, want 202", status)
		}

		if err := <-stopped; !errors.Is(err, tt.wantShutdownError) {
			t.Errorf("Graceful shutdown: %v", err)
		}
	})
	t.Run("httpshutdown deadline", func(t *testing.T) {
		tt := struct {
			shutdownTimeout   time.Duration
			wantShutdownError error
		}{
			shutdownTimeout:   30 * time.Millisecond,
			wantShutdownError: context.DeadlineExceeded,
		}

		entered, canceled := make(chan struct{}), make(chan struct{})
		address, cancel, stopped := startHTTPServer(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			close(entered)
			<-r.Context().Done()
			close(canceled)
		}), tt.shutdownTimeout)
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
		if err := <-stopped; !errors.Is(err, tt.wantShutdownError) {
			t.Errorf("Shutdown error = %v, want deadline exceeded", err)
		}

		select {
		case <-canceled:
		case <-time.After(time.Second):
			t.Fatal("Forced shutdown did not cancel the request")
		}
	})
	t.Run("httpserve failure", func(t *testing.T) {
		tt := struct {
			shutdownTimeout time.Duration
			wantError       bool
		}{
			shutdownTimeout: time.Second,
			wantError:       true,
		}

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("Open test listener: %v", err)
		}

		listener.Close()
		if err := serveUntilStopped(context.Background(), &http.Server{}, listener, tt.shutdownTimeout); (err != nil) != tt.wantError {
			t.Fatal("Closed listener was treated as graceful shutdown")
		}
	})
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
