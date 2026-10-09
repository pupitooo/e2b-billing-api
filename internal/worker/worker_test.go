package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const completionTimeout = 5 * time.Second

type workerRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

// TestWorkerDrainsBacklogImmediately verifies that startup and available batches
// do not wait for the polling interval, and that only one callback runs at a time.
func TestWorkerDrainsBacklogImmediately(t *testing.T) {
	starts := make(chan int32, 8)
	var calls, active atomic.Int32
	var overlapped atomic.Bool
	w := newTestWorker(t)
	w.ProcessBatch = func(ctx context.Context) (bool, error) {
		if active.Add(1) != 1 {
			overlapped.Store(true)
		}
		defer active.Add(-1)
		call := calls.Add(1)
		select {
		case starts <- call:
		case <-ctx.Done():
			return false, ctx.Err()
		}
		if call < 3 {
			return true, nil
		}
		<-ctx.Done()
		return false, ctx.Err()
	}
	run := startWorkerRun(t, w.Run)
	for want := int32(1); want <= 3; want++ {
		if got := awaitValue(t, starts, "next available batch"); got != want {
			t.Fatalf("Batch order = %d, want %d", got, want)
		}
	}
	run.cancel()
	if err := waitForWorkerRun(t, run); err != nil {
		t.Fatalf("Stop worker: %v", err)
	}
	if calls.Load() != 3 || active.Load() != 0 || overlapped.Load() {
		t.Fatalf("Callbacks: calls=%d active=%d overlap=%t; want three serial, completed callbacks",
			calls.Load(), active.Load(), overlapped.Load())
	}
}

// TestWorkerCancellationWhileIdle verifies that an idle worker stops promptly
// instead of waiting for its deliberately long polling interval.
func TestWorkerCancellationWhileIdle(t *testing.T) {
	initialHeartbeat := make(chan string, 1)
	var calls atomic.Int32
	w := newTestWorker(t)
	w.ProcessBatch = func(context.Context) (bool, error) {
		calls.Add(1)
		data, err := os.ReadFile(w.HeartbeatFile)
		if err != nil {
			return false, err
		}
		initialHeartbeat <- string(data)
		return false, nil
	}
	run := startWorkerRun(t, w.Run)
	before := awaitValue(t, initialHeartbeat, "initial idle callback")
	waitForHeartbeatChange(t, w.HeartbeatFile, before)
	run.cancel()
	if err := waitForWorkerRun(t, run); err != nil {
		t.Fatalf("Stop idle worker: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("Idle callbacks = %d, want 1", calls.Load())
	}
}

// TestWorkerCancelsCooperativeBatch verifies that stopping propagates to the
// current batch context and does not start another batch after cancellation.
func TestWorkerCancelsCooperativeBatch(t *testing.T) {
	started := make(chan struct{}, 1)
	batchError := make(chan error, 1)
	var calls atomic.Int32
	w := newTestWorker(t)
	w.ProcessBatch = func(ctx context.Context) (bool, error) {
		calls.Add(1)
		started <- struct{}{}
		<-ctx.Done()
		batchError <- ctx.Err()
		return true, ctx.Err()
	}
	run := startWorkerRun(t, w.Run)
	awaitValue(t, started, "in-flight batch")
	run.cancel()
	if err := awaitValue(t, batchError, "batch cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Batch context error = %v, want context.Canceled", err)
	}
	if err := waitForWorkerRun(t, run); err != nil {
		t.Fatalf("Stop worker during a batch: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("Callbacks after cancellation = %d, want 1", calls.Load())
	}
}

// TestWorkerBatchDeadlineDelaysRetry verifies that a batch receives a deadline,
// and that expiration enforces the retry delay even when it returns true, nil.
func TestWorkerBatchDeadlineDelaysRetry(t *testing.T) {
	type expiration struct {
		deadline time.Time
		finished time.Time
		err      error
	}
	expired := make(chan expiration, 1)
	retried := make(chan time.Time, 1)
	var calls atomic.Int32
	w := newTestWorker(t)
	w.BatchTimeout = 40 * time.Millisecond
	w.PollInterval = 80 * time.Millisecond
	w.ProcessBatch = func(ctx context.Context) (bool, error) {
		if calls.Add(1) == 1 {
			deadline, ok := ctx.Deadline()
			if !ok {
				return false, errors.New("batch context has no deadline")
			}
			<-ctx.Done()
			expired <- expiration{deadline, time.Now(), ctx.Err()}
			return true, nil
		}
		select {
		case retried <- time.Now():
		case <-ctx.Done():
			return false, ctx.Err()
		}
		return false, nil
	}
	run := startWorkerRun(t, w.Run)
	got := awaitValue(t, expired, "batch deadline")
	if !errors.Is(got.err, context.DeadlineExceeded) {
		t.Fatalf("Batch context error = %v, want context.DeadlineExceeded", got.err)
	}
	if got.finished.Before(got.deadline) {
		t.Fatalf("Batch expired at %s before its deadline %s", got.finished, got.deadline)
	}
	if delay := awaitValue(t, retried, "retry after expired batch").Sub(got.finished); delay < w.PollInterval {
		t.Fatalf("Deadline retry delay = %s, want at least %s", delay, w.PollInterval)
	}
	run.cancel()
	if err := waitForWorkerRun(t, run); err != nil {
		t.Fatalf("Stop worker after deadline retry: %v", err)
	}
}

// TestWorkerErrorDelaysRetry verifies that a processor error takes precedence
// over its more-work flag, preventing a tight retry loop while keeping it alive.
func TestWorkerErrorDelaysRetry(t *testing.T) {
	failed := make(chan time.Time, 1)
	retried := make(chan time.Time, 1)
	var calls atomic.Int32
	w := newTestWorker(t)
	w.PollInterval = 80 * time.Millisecond
	w.ProcessBatch = func(ctx context.Context) (bool, error) {
		if calls.Add(1) == 1 {
			failed <- time.Now()
			return true, errors.New("temporary batch failure")
		}
		select {
		case retried <- time.Now():
		case <-ctx.Done():
			return false, ctx.Err()
		}
		return false, nil
	}
	run := startWorkerRun(t, w.Run)
	failedAt := awaitValue(t, failed, "failed batch")
	if delay := awaitValue(t, retried, "retry after failed batch").Sub(failedAt); delay < w.PollInterval {
		t.Fatalf("Error retry delay = %s, want at least %s", delay, w.PollInterval)
	}
	if err := CheckHealth(w.HeartbeatFile, time.Second, time.Now); err != nil {
		t.Fatalf("Retrying worker should still report loop activity: %v", err)
	}
	run.cancel()
	if err := waitForWorkerRun(t, run); err != nil {
		t.Fatalf("Stop worker after error retry: %v", err)
	}
}

// TestWorkerHeartbeatLifecycle verifies that startup publishes a valid UTC
// heartbeat before processing, leaves no temporary file, and removes it on exit.
func TestWorkerHeartbeatLifecycle(t *testing.T) {
	started := make(chan struct{}, 1)
	w := newTestWorker(t)
	w.ProcessBatch = func(ctx context.Context) (bool, error) {
		started <- struct{}{}
		<-ctx.Done()
		return false, ctx.Err()
	}
	run := startWorkerRun(t, w.Run)
	awaitValue(t, started, "heartbeat-backed startup")
	if err := CheckHealth(w.HeartbeatFile, time.Second, time.Now); err != nil {
		t.Fatalf("Running worker heartbeat: %v", err)
	}
	data, err := os.ReadFile(w.HeartbeatFile)
	if err != nil {
		t.Fatalf("Read active heartbeat: %v", err)
	}
	if !strings.HasSuffix(string(data), "Z\n") {
		t.Fatalf("Heartbeat = %q, want a UTC timestamp ending with a newline", data)
	}
	files, err := os.ReadDir(filepath.Dir(w.HeartbeatFile))
	if err != nil || len(files) != 1 || files[0].Name() != filepath.Base(w.HeartbeatFile) {
		t.Fatalf("Active heartbeat directory has %d entries, want only the heartbeat; error=%v", len(files), err)
	}
	run.cancel()
	if err := waitForWorkerRun(t, run); err != nil {
		t.Fatalf("Stop worker: %v", err)
	}
	if _, err := os.Stat(w.HeartbeatFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Heartbeat after shutdown: error=%v, want a removed file", err)
	}
}

// TestWorkerHeartbeatWriteFailureStopsStartup verifies that an unusable health
// path prevents processing and cleans up the attempted atomic replacement file.
func TestWorkerHeartbeatWriteFailureStopsStartup(t *testing.T) {
	for _, scenario := range []string{"missing parent", "target is a directory"} {
		t.Run(scenario, func(t *testing.T) {
			w := newTestWorker(t)
			parent := filepath.Dir(w.HeartbeatFile)
			if scenario == "missing parent" {
				w.HeartbeatFile = filepath.Join(parent, "missing", "heartbeat")
			} else if err := os.Mkdir(w.HeartbeatFile, 0700); err != nil {
				t.Fatalf("Create unusable heartbeat target: %v", err)
			}
			var calls int
			w.ProcessBatch = func(context.Context) (bool, error) {
				calls++
				return false, nil
			}
			if err := w.Run(context.Background()); err == nil {
				t.Fatal("Run should fail when its initial heartbeat cannot be written")
			}
			if calls != 0 {
				t.Fatalf("Callbacks after failed startup = %d, want 0", calls)
			}
			files, err := os.ReadDir(parent)
			if err != nil {
				t.Fatalf("Inspect failed heartbeat directory: %v", err)
			}
			for _, file := range files {
				if strings.HasPrefix(file.Name(), ".billing-worker-heartbeat-") {
					t.Fatalf("Failed heartbeat left temporary file %q", file.Name())
				}
			}
		})
	}
}

// TestCheckHealth verifies timestamp freshness, the inclusive maximum-age
// boundary, supported offsets, malformed data, missing files, and invalid ages.
func TestCheckHealth(t *testing.T) {
	now := time.Date(2_026, time.October, 9, 12, 0, 0, 0, time.UTC)
	const maxAge = 5 * time.Second
	tests := []struct {
		name    string
		data    string
		maxAge  time.Duration
		missing bool
		wantErr bool
	}{
		{name: "fresh", data: now.Add(-time.Second).Format(time.RFC3339Nano), maxAge: maxAge},
		{name: "maximum age", data: now.Add(-maxAge).Format(time.RFC3339Nano), maxAge: maxAge},
		{name: "surrounding whitespace", data: "\n\t" + now.Format(time.RFC3339Nano) + " \n", maxAge: maxAge},
		{name: "offset timestamp", data: now.In(time.FixedZone("UTC+2", 2*60*60)).Format(time.RFC3339Nano), maxAge: maxAge},
		{name: "stale", data: now.Add(-maxAge - time.Nanosecond).Format(time.RFC3339Nano), maxAge: maxAge, wantErr: true},
		{name: "future", data: now.Add(time.Nanosecond).Format(time.RFC3339Nano), maxAge: maxAge, wantErr: true},
		{name: "malformed", data: "not a timestamp", maxAge: maxAge, wantErr: true},
		{name: "empty", maxAge: maxAge, wantErr: true},
		{name: "missing", maxAge: maxAge, missing: true, wantErr: true},
		{name: "zero maximum age", data: now.Format(time.RFC3339Nano), wantErr: true},
		{name: "negative maximum age", data: now.Format(time.RFC3339Nano), maxAge: -time.Second, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "heartbeat")
			if !tt.missing {
				if err := os.WriteFile(filename, []byte(tt.data), 0600); err != nil {
					t.Fatalf("Write heartbeat fixture: %v", err)
				}
			}
			if err := CheckHealth(filename, tt.maxAge, func() time.Time { return now }); (err != nil) != tt.wantErr {
				t.Fatalf("CheckHealth error = %v, want error=%t", err, tt.wantErr)
			}
		})
	}
}

// TestCheckHealthConcurrentUpdate verifies that a heartbeat published while the
// probe samples its clock cannot invalidate the already-read healthy snapshot.
func TestCheckHealthConcurrentUpdate(t *testing.T) {
	now := time.Date(2_026, time.October, 9, 12, 0, 0, 0, time.UTC)
	filename := filepath.Join(t.TempDir(), "heartbeat")
	if err := os.WriteFile(filename, []byte(now.Add(-time.Second).Format(time.RFC3339Nano)), 0600); err != nil {
		t.Fatalf("Write initial heartbeat: %v", err)
	}
	clock := func() time.Time {
		if err := os.WriteFile(filename, []byte(now.Add(time.Hour).Format(time.RFC3339Nano)), 0600); err != nil {
			t.Fatalf("Publish concurrent heartbeat: %v", err)
		}
		return now
	}
	if err := CheckHealth(filename, 5*time.Second, clock); err != nil {
		t.Fatalf("Concurrent replacement invalidated a healthy snapshot: %v", err)
	}
}

// TestWorkerRejectsInvalidConfiguration verifies that missing dependencies and
// nonpositive durations fail before processing or publishing a heartbeat.
func TestWorkerRejectsInvalidConfiguration(t *testing.T) {
	for _, scenario := range []string{"no processor", "no logger", "zero poll", "negative poll", "zero batch timeout", "negative batch timeout", "no heartbeat file"} {
		t.Run(scenario, func(t *testing.T) {
			w := newTestWorker(t)
			filename := w.HeartbeatFile
			var calls int
			w.ProcessBatch = func(context.Context) (bool, error) {
				calls++
				return false, nil
			}
			switch scenario {
			case "no processor":
				w.ProcessBatch = nil
			case "no logger":
				w.Logger = nil
			case "zero poll":
				w.PollInterval = 0
			case "negative poll":
				w.PollInterval = -time.Second
			case "zero batch timeout":
				w.BatchTimeout = 0
			case "negative batch timeout":
				w.BatchTimeout = -time.Second
			case "no heartbeat file":
				w.HeartbeatFile = ""
			}
			if err := w.Run(context.Background()); err == nil {
				t.Fatal("Run should reject invalid configuration")
			}
			if calls != 0 {
				t.Fatalf("Callbacks with invalid configuration = %d, want 0", calls)
			}
			if _, err := os.Stat(filename); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Invalid configuration published a heartbeat: %v", err)
			}
		})
	}
}

// TestRunUntilStoppedBoundsUncooperativeShutdown verifies that a processor
// ignoring cancellation cannot hold shutdown forever, then releases it safely.
func TestRunUntilStoppedBoundsUncooperativeShutdown(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	exited := make(chan struct{})
	var releaseOnce sync.Once
	w := newTestWorker(t)
	w.ProcessBatch = func(context.Context) (bool, error) {
		started <- struct{}{}
		<-release
		close(exited)
		return false, nil
	}
	const shutdownTimeout = 40 * time.Millisecond
	run := startWorkerRun(t, func(ctx context.Context) error {
		return RunUntilStopped(ctx, w, shutdownTimeout)
	})
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		awaitValue(t, exited, "released uncooperative callback")
		waitForHeartbeatRemoval(t, w.HeartbeatFile)
	})
	awaitValue(t, started, "uncooperative batch")
	canceledAt := time.Now()
	run.cancel()
	err := waitForWorkerRun(t, run)
	if err == nil || !strings.Contains(err.Error(), "did not stop within") {
		t.Fatalf("Uncooperative shutdown error = %v, want a shutdown deadline failure", err)
	}
	if elapsed := time.Since(canceledAt); elapsed < shutdownTimeout {
		t.Fatalf("Shutdown waited %s, want at least %s", elapsed, shutdownTimeout)
	}
	select {
	case <-exited:
		t.Fatal("Uncooperative callback should remain blocked until explicitly released")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	awaitValue(t, exited, "uncooperative callback exit")
	waitForHeartbeatRemoval(t, w.HeartbeatFile)
}

// TestRunUntilStoppedRejectsInvalidTimeout verifies that a nonpositive shutdown
// limit fails before starting the worker goroutine or publishing a heartbeat.
func TestRunUntilStoppedRejectsInvalidTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		t.Run(timeout.String(), func(t *testing.T) {
			w := newTestWorker(t)
			w.ProcessBatch = func(context.Context) (bool, error) {
				t.Error("Invalid shutdown timeout must not invoke the processor")
				return false, nil
			}
			if err := RunUntilStopped(context.Background(), w, timeout); err == nil {
				t.Fatal("RunUntilStopped should reject a nonpositive timeout")
			}
			if _, err := os.Stat(w.HeartbeatFile); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Invalid shutdown timeout published a heartbeat: %v", err)
			}
		})
	}
}

// newTestWorker provides a quiet worker with a private health file and a long
// idle interval, so lifecycle tests can distinguish immediate work from polling.
func newTestWorker(t *testing.T) Worker {
	t.Helper()
	return Worker{
		PollInterval:  time.Hour,
		BatchTimeout:  10 * time.Second,
		HeartbeatFile: filepath.Join(t.TempDir(), "heartbeat"),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// startWorkerRun launches a lifecycle function with cancellation and arranges
// bounded cleanup, allowing failed assertions to stop cooperative test workers.
func startWorkerRun(t *testing.T, run func(context.Context) error) *workerRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	result := &workerRun{cancel: cancel, done: make(chan struct{})}
	go func() {
		result.err = run(ctx)
		close(result.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-result.done:
		case <-time.After(completionTimeout):
			t.Error("Worker did not stop during test cleanup")
		}
	})
	return result
}

// waitForWorkerRun waits for a lifecycle result with a generous upper bound so
// incorrect cancellation cannot hang the entire package test suite.
func waitForWorkerRun(t *testing.T, run *workerRun) error {
	t.Helper()
	awaitValue(t, run.done, "worker completion")
	return run.err
}

// awaitValue coordinates test callbacks through channels and fails on a bounded
// timeout rather than leaving a lifecycle test blocked indefinitely.
func awaitValue[T any](t *testing.T, values <-chan T, description string) T {
	t.Helper()
	timer := time.NewTimer(completionTimeout)
	defer timer.Stop()
	select {
	case value := <-values:
		return value
	case <-timer.C:
		t.Fatalf("Timed out waiting for %s", description)
		var zero T
		return zero
	}
}

// waitForHeartbeatChange observes completion of an idle iteration through its
// heartbeat replacement, avoiding a fixed sleep before exercising cancellation.
func waitForHeartbeatChange(t *testing.T, filename, previous string) {
	t.Helper()
	timer := time.NewTimer(completionTimeout)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(filename)
		if err != nil {
			t.Fatalf("Read heartbeat while waiting for an idle iteration: %v", err)
		}
		if string(data) != previous {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatal("Idle iteration did not replace its heartbeat")
		}
	}
}

// waitForHeartbeatRemoval waits for the abandoned loop to unwind after its
// test callback is released, proving cleanup without leaving background work.
func waitForHeartbeatRemoval(t *testing.T, filename string) {
	t.Helper()
	timer := time.NewTimer(completionTimeout)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		_, err := os.Stat(filename)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if err != nil {
			t.Fatalf("Inspect heartbeat cleanup: %v", err)
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatal("Released worker did not remove its heartbeat")
		}
	}
}
