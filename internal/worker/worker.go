// Package worker provides the lifecycle of the standalone billing worker.
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Worker runs one batch at a time. ProcessBatch reports whether more work is
// available, so a backlog can be drained without waiting between batches.
// Implementations must respect the context and commit accounting with the inbox
// completion marker in one transaction before reporting a batch as processed.
type Worker struct {
	ProcessBatch  func(context.Context) (bool, error)
	PollInterval  time.Duration
	BatchTimeout  time.Duration
	HeartbeatFile string
	Logger        *slog.Logger
}

// Run starts immediately, waits after idle or failed iterations, and removes its
// heartbeat on exit. A heartbeat measures loop activity, not accounting success.
func (w Worker) Run(ctx context.Context) error {
	if err := w.validate(); err != nil {
		return err
	}
	if err := writeHeartbeat(w.HeartbeatFile); err != nil {
		return err
	}
	defer os.Remove(w.HeartbeatFile)

	for ctx.Err() == nil {
		more, err := w.processNextBatch(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err := writeHeartbeat(w.HeartbeatFile); err != nil {
			return err
		}
		if err != nil {
			w.Logger.Error("Worker batch failed; retrying after the polling interval", "error", err)
		}
		if !more || err != nil {
			waitForPoll(ctx, w.PollInterval)
		}
	}
	return nil
}

func (w Worker) validate() error {
	if w.ProcessBatch == nil || w.Logger == nil {
		return fmt.Errorf("worker requires a batch processor and logger")
	}
	if w.PollInterval <= 0 || w.BatchTimeout <= 0 || w.HeartbeatFile == "" {
		return fmt.Errorf("worker requires positive intervals and a heartbeat file")
	}
	return nil
}

// processNextBatch owns the batch deadline, including processors that return
// success after their context expires. Lifecycle cancellation stays in Run.
func (w Worker) processNextBatch(ctx context.Context) (bool, error) {
	batchCtx, cancel := context.WithTimeout(ctx, w.BatchTimeout)
	defer cancel()
	more, err := w.ProcessBatch(batchCtx)
	if err == nil {
		err = batchCtx.Err()
	}
	return more, err
}

// waitForPoll ends on either the interval or cancellation. The processing loop
// checks cancellation before starting another batch and removes its heartbeat.
func waitForPoll(ctx context.Context, interval time.Duration) {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// RunUntilStopped bounds shutdown even if a processor ignores cancellation.
// The executable must exit on this error; it must not start another loop while
// an abandoned processor is still running.
func RunUntilStopped(ctx context.Context, w Worker, shutdownTimeout time.Duration) error {
	if shutdownTimeout <= 0 {
		return fmt.Errorf("worker shutdown timeout must be positive")
	}
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		timer := time.NewTimer(shutdownTimeout)
		defer timer.Stop()
		select {
		case err := <-done:
			return err
		case <-timer.C:
			return fmt.Errorf("worker did not stop within %s", shutdownTimeout)
		}
	}
}

// CheckHealth checks the processing loop's most recent heartbeat. It makes no
// claim about database availability, pending work, or financial correctness.
func CheckHealth(filename string, maxAge time.Duration, now func() time.Time) error {
	if maxAge <= 0 || now == nil {
		return fmt.Errorf("heartbeat requires a positive maximum age and a clock")
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("read worker heartbeat: %w", err)
	}
	last, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if err != nil {
		return fmt.Errorf("parse worker heartbeat: %w", err)
	}
	// Sample after the read: an atomic replacement may contain a timestamp newer
	// than a time captured by the caller before reading the file.
	age := now().Sub(last)
	if age < 0 || age > maxAge {
		return fmt.Errorf("worker heartbeat is outside the allowed age of %s", maxAge)
	}
	return nil
}

// Replace the heartbeat atomically so exec health checks never read a partial
// timestamp. A stale file also exposes a processor stuck inside a batch.
func writeHeartbeat(filename string) error {
	file, err := os.CreateTemp(filepath.Dir(filename), ".billing-worker-heartbeat-")
	if err != nil {
		return fmt.Errorf("create worker heartbeat: %w", err)
	}
	defer os.Remove(file.Name())
	if _, err := file.WriteString(time.Now().UTC().Format(time.RFC3339Nano) + "\n"); err != nil {
		file.Close()
		return fmt.Errorf("write worker heartbeat: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close worker heartbeat: %w", err)
	}
	if err := os.Rename(file.Name(), filename); err != nil {
		return fmt.Errorf("replace worker heartbeat: %w", err)
	}
	return nil
}
