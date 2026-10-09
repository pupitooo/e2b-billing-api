package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/httpapi"
	"e2b/billing-api/internal/inbox"
)

func main() {
	if err := run(os.Getenv); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

// run owns resources so its deferred cleanup finishes before main can exit.
func run(getenv func(string) string) error {
	cfg, err := loadConfig(getenv)
	if err != nil {
		return fmt.Errorf("invalid API configuration: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, cfg.startupTimeout)
	pool, err := inbox.OpenPool(startup, getenv("DATABASE_URL"), inbox.PoolLimits{
		MaxConns: cfg.poolMaxConns, MinConns: cfg.poolMinConns,
	})
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("initialize inbox: %w", err)
	}
	defer func() {
		pool.Close()
		log.Print("Inbox database pool closed")
	}()

	server := &http.Server{
		Addr:              ":8080",
		Handler:           httpapi.NewHandler(inbox.NewPostgres(pool, cfg.rollbackTimeout), cfg.ingestionTimeout, int(cfg.maxInFlight), billing.NewStore(pool)),
		ReadHeaderTimeout: cfg.readHeaderTimeout,
		ReadTimeout:       cfg.readTimeout,
		WriteTimeout:      cfg.writeTimeout,
		IdleTimeout:       cfg.idleTimeout,
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen for HTTP requests: %w", err)
	}

	log.Printf("Billing API listening on %s", server.Addr)
	log.Printf("API limits: read_header=%s read=%s write=%s idle=%s ingestion=%s rollback=%s shutdown=%s db_min=%d db_max=%d in_flight=%d",
		cfg.readHeaderTimeout, cfg.readTimeout, cfg.writeTimeout, cfg.idleTimeout,
		cfg.ingestionTimeout, cfg.rollbackTimeout, cfg.shutdownTimeout, cfg.poolMinConns, cfg.poolMaxConns, cfg.maxInFlight)
	return serveUntilStopped(ctx, server, listener, cfg.shutdownTimeout)
}

// serveUntilStopped drains requests before the owner closes its database pool.
// Shutdown has an independent context because the signal context is canceled.
func serveUntilStopped(ctx context.Context, server *http.Server, listener net.Listener, timeout time.Duration) error {
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(listener) }()
	var serveErr error
	select {
	case serveErr = <-stopped:
	case <-ctx.Done():
		log.Print("Stopping HTTP API; waiting for active requests")
	}
	shutdown, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		// Closing sockets cancels request contexts; storage then rolls back or
		// returns its committed outcome before pool.Close can finish.
		_ = server.Close()
		return fmt.Errorf("shut down HTTP server: %w", err)
	}
	if serveErr == nil {
		serveErr = <-stopped
	}
	if !errors.Is(serveErr, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP requests: %w", serveErr)
	}
	return nil
}
