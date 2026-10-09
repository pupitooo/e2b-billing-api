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

// Worker.Run drains available batches serially, bounds and delays failed work,
// propagates cancellation, and maintains its heartbeat for each declared scenario.
func TestWorkerRun(t *testing.T) {
	t.Run("worker drains backlog immediately", func(t *testing.T) {
		tt := struct {
			pollInterval            time.Duration
			availableBatches        int32
			wantBatchOrder          []int32
			wantCalls               int32
			wantActiveAfterShutdown int32
			wantOverlap             bool
		}{
			pollInterval:            time.Hour,
			availableBatches:        2,
			wantBatchOrder:          []int32{1, 2, 3},
			wantCalls:               3,
			wantActiveAfterShutdown: 0,
			wantOverlap:             false,
		}

		starts := make(chan int32, 8)
		var calls, active atomic.Int32
		var overlapped atomic.Bool
		w := newTestWorker(t)
		w.PollInterval = tt.pollInterval
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
			if call <= tt.availableBatches {
				return true, nil
			}
			<-ctx.Done()
			return false, ctx.Err()
		}
		run := startWorkerRun(t, w.Run)
		for _, want := range tt.wantBatchOrder {
			if got := awaitValue(t, starts, "next available batch"); got != want {
				t.Fatalf("Batch order = %d, want %d", got, want)
			}
		}
		run.cancel()
		if err := waitForWorkerRun(t, run); err != nil {
			t.Fatalf("Stop worker: %v", err)
		}
		if calls.Load() != tt.wantCalls || active.Load() != tt.wantActiveAfterShutdown || overlapped.Load() != tt.wantOverlap {
			t.Fatalf("Callbacks: calls=%d active=%d overlap=%t; want three serial, completed callbacks",
				calls.Load(), active.Load(), overlapped.Load())
		}
	})
	t.Run("worker cancellation while idle", func(t *testing.T) {
		tt := struct {
			pollInterval time.Duration
			batchHasMore bool
			wantCalls    int32
		}{
			pollInterval: time.Hour,
			batchHasMore: false,
			wantCalls:    1,
		}

		initialHeartbeat := make(chan string, 1)
		var calls atomic.Int32
		w := newTestWorker(t)
		w.PollInterval = tt.pollInterval
		w.ProcessBatch = func(context.Context) (bool, error) {
			calls.Add(1)
			data, err := os.ReadFile(w.HeartbeatFile)
			if err != nil {
				return false, err
			}
			initialHeartbeat <- string(data)
			return tt.batchHasMore, nil
		}
		run := startWorkerRun(t, w.Run)
		before := awaitValue(t, initialHeartbeat, "initial idle callback")
		waitForHeartbeatChange(t, w.HeartbeatFile, before)
		run.cancel()
		if err := waitForWorkerRun(t, run); err != nil {
			t.Fatalf("Stop idle worker: %v", err)
		}
		if calls.Load() != tt.wantCalls {
			t.Fatalf("Idle callbacks = %d, want 1", calls.Load())
		}
	})
	t.Run("worker cancels cooperative batch", func(t *testing.T) {
		tt := struct {
			batchHasMore   bool
			wantBatchError error
			wantCalls      int32
		}{
			batchHasMore:   true,
			wantBatchError: context.Canceled,
			wantCalls:      1,
		}

		started := make(chan struct{}, 1)
		batchError := make(chan error, 1)
		var calls atomic.Int32
		w := newTestWorker(t)
		w.ProcessBatch = func(ctx context.Context) (bool, error) {
			calls.Add(1)
			started <- struct{}{}
			<-ctx.Done()
			batchError <- ctx.Err()
			return tt.batchHasMore, ctx.Err()
		}
		run := startWorkerRun(t, w.Run)
		awaitValue(t, started, "in-flight batch")
		run.cancel()
		if err := awaitValue(t, batchError, "batch cancellation"); !errors.Is(err, tt.wantBatchError) {
			t.Fatalf("Batch context error = %v, want context.Canceled", err)
		}
		if err := waitForWorkerRun(t, run); err != nil {
			t.Fatalf("Stop worker during a batch: %v", err)
		}
		if calls.Load() != tt.wantCalls {
			t.Fatalf("Callbacks after cancellation = %d, want 1", calls.Load())
		}
	})
	t.Run("worker batch deadline delays retry", func(t *testing.T) {
		tt := struct {
			batchTimeout          time.Duration
			pollInterval          time.Duration
			batchHasMore          bool
			wantBatchError        error
			wantMinimumRetryDelay time.Duration
		}{
			batchTimeout:          40 * time.Millisecond,
			pollInterval:          80 * time.Millisecond,
			batchHasMore:          true,
			wantBatchError:        context.DeadlineExceeded,
			wantMinimumRetryDelay: 80 * time.Millisecond,
		}

		type expiration struct {
			deadline time.Time
			finished time.Time
			err      error
		}
		expired := make(chan expiration, 1)
		retried := make(chan time.Time, 1)
		var calls atomic.Int32
		w := newTestWorker(t)
		w.BatchTimeout = tt.batchTimeout
		w.PollInterval = tt.pollInterval
		w.ProcessBatch = func(ctx context.Context) (bool, error) {
			if calls.Add(1) == 1 {
				deadline, ok := ctx.Deadline()
				if !ok {
					return false, errors.New("batch context has no deadline")
				}
				<-ctx.Done()
				expired <- expiration{deadline: deadline, finished: time.Now(), err: ctx.Err()}
				return tt.batchHasMore, nil
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
		if !errors.Is(got.err, tt.wantBatchError) {
			t.Fatalf("Batch context error = %v, want context.DeadlineExceeded", got.err)
		}
		if got.finished.Before(got.deadline) {
			t.Fatalf("Batch expired at %s before its deadline %s", got.finished, got.deadline)
		}
		if delay := awaitValue(t, retried, "retry after expired batch").Sub(got.finished); delay < tt.wantMinimumRetryDelay {
			t.Fatalf("Deadline retry delay = %s, want at least %s", delay, w.PollInterval)
		}
		run.cancel()
		if err := waitForWorkerRun(t, run); err != nil {
			t.Fatalf("Stop worker after deadline retry: %v", err)
		}
	})
	t.Run("worker error delays retry", func(t *testing.T) {
		tt := struct {
			pollInterval          time.Duration
			batchHasMore          bool
			batchError            error
			wantMinimumRetryDelay time.Duration
		}{
			pollInterval:          80 * time.Millisecond,
			batchHasMore:          true,
			batchError:            errors.New("temporary batch failure"),
			wantMinimumRetryDelay: 80 * time.Millisecond,
		}

		failed := make(chan time.Time, 1)
		retried := make(chan time.Time, 1)
		var calls atomic.Int32
		w := newTestWorker(t)
		w.PollInterval = tt.pollInterval
		w.ProcessBatch = func(ctx context.Context) (bool, error) {
			if calls.Add(1) == 1 {
				failed <- time.Now()
				return tt.batchHasMore, tt.batchError
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
		if delay := awaitValue(t, retried, "retry after failed batch").Sub(failedAt); delay < tt.wantMinimumRetryDelay {
			t.Fatalf("Error retry delay = %s, want at least %s", delay, w.PollInterval)
		}
		if err := CheckHealth(w.HeartbeatFile, time.Second, time.Now); err != nil {
			t.Fatalf("Retrying worker should still report loop activity: %v", err)
		}
		run.cancel()
		if err := waitForWorkerRun(t, run); err != nil {
			t.Fatalf("Stop worker after error retry: %v", err)
		}
	})
	t.Run("worker heartbeat lifecycle", func(t *testing.T) {
		tt := struct {
			wantTimestampSuffix      string
			wantActiveFiles          int
			wantMissingAfterShutdown bool
		}{
			wantTimestampSuffix:      "Z\n",
			wantActiveFiles:          1,
			wantMissingAfterShutdown: true,
		}

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
		if !strings.HasSuffix(string(data), tt.wantTimestampSuffix) {
			t.Fatalf("Heartbeat = %q, want a UTC timestamp ending with a newline", data)
		}
		files, err := os.ReadDir(filepath.Dir(w.HeartbeatFile))
		if err != nil || len(files) != tt.wantActiveFiles || files[0].Name() != filepath.Base(w.HeartbeatFile) {
			t.Fatalf("Active heartbeat directory has %d entries, want only the heartbeat; error=%v", len(files), err)
		}
		run.cancel()
		if err := waitForWorkerRun(t, run); err != nil {
			t.Fatalf("Stop worker: %v", err)
		}
		if _, err := os.Stat(w.HeartbeatFile); errors.Is(err, os.ErrNotExist) != tt.wantMissingAfterShutdown {
			t.Fatalf("Heartbeat after shutdown: error=%v, want a removed file", err)
		}
	})
	t.Run("worker heartbeat write failure stops startup", func(t *testing.T) {
		tests := []struct {
			name               string
			missingParent      bool
			wantErroror        bool
			wantCalls          int
			wantTemporaryFiles int
		}{
			{
				name:               "missing parent",
				missingParent:      true,
				wantErroror:        true,
				wantCalls:          0,
				wantTemporaryFiles: 0,
			},
			{
				name:               "target is a directory",
				missingParent:      false,
				wantErroror:        true,
				wantCalls:          0,
				wantTemporaryFiles: 0,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				w := newTestWorker(t)
				parent := filepath.Dir(w.HeartbeatFile)
				if tt.missingParent {
					w.HeartbeatFile = filepath.Join(parent, "missing", "heartbeat")
				} else if err := os.Mkdir(w.HeartbeatFile, 0700); err != nil {
					t.Fatalf("Create unusable heartbeat target: %v", err)
				}
				var calls int
				w.ProcessBatch = func(context.Context) (bool, error) {
					calls++
					return false, nil
				}
				if err := w.Run(context.Background()); (err != nil) != tt.wantErroror {
					t.Fatal("Run should fail when its initial heartbeat cannot be written")
				}
				if calls != tt.wantCalls {
					t.Fatalf("Callbacks after failed startup = %d, want 0", calls)
				}
				files, err := os.ReadDir(parent)
				if err != nil {
					t.Fatalf("Inspect failed heartbeat directory: %v", err)
				}
				temporaryFiles := 0
				for _, file := range files {
					if strings.HasPrefix(file.Name(), ".billing-worker-heartbeat-") {
						temporaryFiles++
					}
				}
				if temporaryFiles != tt.wantTemporaryFiles {
					t.Errorf("Temporary heartbeat files = %d; want %d", temporaryFiles, tt.wantTemporaryFiles)
				}
			})
		}
	})
	t.Run("worker rejects invalid configuration", func(t *testing.T) {
		tests := []struct {
			name                 string
			change               func(*Worker)
			wantError            bool
			wantCalls            int
			wantHeartbeatMissing bool
		}{
			{
				name:                 "no processor",
				change:               func(w *Worker) { w.ProcessBatch = nil },
				wantError:            true,
				wantCalls:            0,
				wantHeartbeatMissing: true,
			},
			{
				name:                 "no logger",
				change:               func(w *Worker) { w.Logger = nil },
				wantError:            true,
				wantCalls:            0,
				wantHeartbeatMissing: true,
			},
			{
				name:                 "zero poll",
				change:               func(w *Worker) { w.PollInterval = 0 },
				wantError:            true,
				wantCalls:            0,
				wantHeartbeatMissing: true,
			},
			{
				name:                 "negative poll",
				change:               func(w *Worker) { w.PollInterval = -time.Second },
				wantError:            true,
				wantCalls:            0,
				wantHeartbeatMissing: true,
			},
			{
				name:                 "zero batch timeout",
				change:               func(w *Worker) { w.BatchTimeout = 0 },
				wantError:            true,
				wantCalls:            0,
				wantHeartbeatMissing: true,
			},
			{
				name:                 "negative batch timeout",
				change:               func(w *Worker) { w.BatchTimeout = -time.Second },
				wantError:            true,
				wantCalls:            0,
				wantHeartbeatMissing: true,
			},
			{
				name:                 "no heartbeat file",
				change:               func(w *Worker) { w.HeartbeatFile = "" },
				wantError:            true,
				wantCalls:            0,
				wantHeartbeatMissing: true,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				w := newTestWorker(t)
				filename := w.HeartbeatFile
				calls := 0
				w.ProcessBatch = func(context.Context) (bool, error) { calls++; return false, nil }
				tt.change(&w)

				err := w.Run(context.Background())
				if (err != nil) != tt.wantError {
					t.Errorf("Run error = %v; want error=%t", err, tt.wantError)
				}
				if calls != tt.wantCalls {
					t.Errorf("ProcessBatch calls = %d; want %d", calls, tt.wantCalls)
				}
				_, err = os.Stat(filename)
				if errors.Is(err, os.ErrNotExist) != tt.wantHeartbeatMissing {
					t.Errorf("Heartbeat stat error = %v; want missing=%t", err, tt.wantHeartbeatMissing)
				}
			})
		}
	})
}

// CheckHealth accepts only readable, well-formed, fresh timestamps and retains
// a healthy snapshot if a concurrent writer replaces its file after reading.
func TestCheckHealth(t *testing.T) {
	t.Run("check health", func(t *testing.T) {
		now := time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC)
		const maxAge = 5 * time.Second
		tests := []struct {
			name      string
			data      string
			maxAge    time.Duration
			missing   bool
			wantError bool
		}{
			{
				name:      "fresh",
				data:      now.Add(-time.Second).Format(time.RFC3339Nano),
				maxAge:    maxAge,
				wantError: false,
			},
			{
				name:      "maximum age",
				data:      now.Add(-maxAge).Format(time.RFC3339Nano),
				maxAge:    maxAge,
				wantError: false,
			},
			{
				name:      "surrounding whitespace",
				data:      "\n\t" + now.Format(time.RFC3339Nano) + " \n",
				maxAge:    maxAge,
				wantError: false,
			},
			{
				name:      "offset timestamp",
				data:      now.In(time.FixedZone("UTC+2", 2*60*60)).Format(time.RFC3339Nano),
				maxAge:    maxAge,
				wantError: false,
			},
			{
				name:      "stale",
				data:      now.Add(-maxAge - time.Nanosecond).Format(time.RFC3339Nano),
				maxAge:    maxAge,
				wantError: true,
			},
			{
				name:      "future",
				data:      now.Add(time.Nanosecond).Format(time.RFC3339Nano),
				maxAge:    maxAge,
				wantError: true,
			},
			{
				name:      "malformed",
				data:      "not a timestamp",
				maxAge:    maxAge,
				wantError: true,
			},
			{
				name:      "empty",
				maxAge:    maxAge,
				wantError: true,
			},
			{
				name:      "missing",
				maxAge:    maxAge,
				missing:   true,
				wantError: true,
			},
			{
				name:      "zero maximum age",
				data:      now.Format(time.RFC3339Nano),
				wantError: true,
			},
			{
				name:      "negative maximum age",
				data:      now.Format(time.RFC3339Nano),
				maxAge:    -time.Second,
				wantError: true,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				filename := filepath.Join(t.TempDir(), "heartbeat")
				if !tt.missing {
					if err := os.WriteFile(filename, []byte(tt.data), 0600); err != nil {
						t.Fatalf("Write heartbeat fixture: %v", err)
					}
				}
				if err := CheckHealth(filename, tt.maxAge, func() time.Time { return now }); (err != nil) != tt.wantError {
					t.Fatalf("CheckHealth error = %v, want error=%t", err, tt.wantError)
				}
			})
		}
	})
	t.Run("check health concurrent update", func(t *testing.T) {
		tt := struct {
			now                    time.Time
			initialAge             time.Duration
			concurrentFutureOffset time.Duration
			maxAge                 time.Duration
			wantError              bool
		}{
			now:                    time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC),
			initialAge:             time.Second,
			concurrentFutureOffset: time.Hour,
			maxAge:                 5 * time.Second,
			wantError:              false,
		}

		now := tt.now
		filename := filepath.Join(t.TempDir(), "heartbeat")
		if err := os.WriteFile(filename, []byte(now.Add(-tt.initialAge).Format(time.RFC3339Nano)), 0600); err != nil {
			t.Fatalf("Write initial heartbeat: %v", err)
		}
		clock := func() time.Time {
			if err := os.WriteFile(filename, []byte(now.Add(tt.concurrentFutureOffset).Format(time.RFC3339Nano)), 0600); err != nil {
				t.Fatalf("Publish concurrent heartbeat: %v", err)
			}
			return now
		}
		if err := CheckHealth(filename, tt.maxAge, clock); (err != nil) != tt.wantError {
			t.Fatalf("Concurrent replacement invalidated a healthy snapshot: %v", err)
		}
	})
}

// RunUntilStopped enforces the declared shutdown deadline for an uncooperative
// callback and rejects invalid timeouts before starting a worker.
func TestRunUntilStopped(t *testing.T) {
	t.Run("run until stopped bounds uncooperative shutdown", func(t *testing.T) {
		tt := struct {
			shutdownTimeout         time.Duration
			wantErrorContains       string
			wantMinimumShutdownWait time.Duration
		}{
			shutdownTimeout:         40 * time.Millisecond,
			wantErrorContains:       "did not stop within",
			wantMinimumShutdownWait: 40 * time.Millisecond,
		}

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
		shutdownTimeout := tt.shutdownTimeout
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
		if err == nil || !strings.Contains(err.Error(), tt.wantErrorContains) {
			t.Fatalf("Uncooperative shutdown error = %v, want a shutdown deadline failure", err)
		}
		if elapsed := time.Since(canceledAt); elapsed < tt.wantMinimumShutdownWait {
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
	})
	t.Run("run until stopped rejects invalid timeout", func(t *testing.T) {
		tests := []struct {
			name                 string
			shutdownTimeout      time.Duration
			wantError            bool
			wantHeartbeatMissing bool
		}{
			{
				name:                 "zero timeout",
				shutdownTimeout:      0,
				wantError:            true,
				wantHeartbeatMissing: true,
			},
			{
				name:                 "negative timeout",
				shutdownTimeout:      -time.Second,
				wantError:            true,
				wantHeartbeatMissing: true,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				w := newTestWorker(t)
				w.ProcessBatch = func(context.Context) (bool, error) {
					t.Error("Invalid shutdown timeout must not invoke the processor")
					return false, nil
				}
				if err := RunUntilStopped(context.Background(), w, tt.shutdownTimeout); (err != nil) != tt.wantError {
					t.Fatal("RunUntilStopped should reject a nonpositive timeout")
				}
				if _, err := os.Stat(w.HeartbeatFile); errors.Is(err, os.ErrNotExist) != tt.wantHeartbeatMissing {
					t.Fatalf("Invalid shutdown timeout published a heartbeat: %v", err)
				}
			})
		}
	})
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
