//go:build integration

package worker_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The worker executable publishes health before processing, exits cleanly on
// SIGTERM, and rejects invalid startup without leaving a healthy heartbeat.
func TestWorkerExecutable(t *testing.T) {
	t.Run("worker process lifecycle", func(t *testing.T) {
		tt := struct {
			environment           []string
			signal                os.Signal
			wantExitError         bool
			wantHeartbeatMissing  bool
			wantStoppedProbeError bool
			wantStartupOutput     string
		}{
			environment:           []string{"E2B_WORKER_POLL_INTERVAL=1h", "E2B_WORKER_BATCH_TIMEOUT=5s", "E2B_WORKER_SHUTDOWN_TIMEOUT=200ms", "E2B_WORKER_HEARTBEAT_MAX_AGE=2h"},
			signal:                syscall.SIGTERM,
			wantExitError:         false,
			wantHeartbeatMissing:  true,
			wantStoppedProbeError: true,
			wantStartupOutput:     `"accounting_enabled":false`,
		}

		binary := workerBinary(t)
		heartbeat := filepath.Join(t.TempDir(), "heartbeat")
		process := exec.Command(binary)
		process.Env = append(append(os.Environ(), tt.environment...), "E2B_WORKER_HEARTBEAT_FILE="+heartbeat)
		var output bytes.Buffer
		process.Stdout, process.Stderr = &output, &output
		if err := process.Start(); err != nil {
			t.Fatalf("Start standalone worker: %v", err)
		}
		done := make(chan error, 1)
		go func() { done <- process.Wait() }()
		stopped := false
		t.Cleanup(func() {
			if !stopped {
				process.Process.Kill()
				<-done
			}
		})
		waitForHealthyWorker(t, binary, process.Env)
		if err := process.Process.Signal(tt.signal); err != nil {
			t.Fatalf("Send SIGTERM: %v", err)
		}
		select {
		case err := <-done:
			stopped = true
			if (err != nil) != tt.wantExitError {
				t.Fatalf("Worker must exit successfully on SIGTERM: %v\n%s", err, &output)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Worker did not stop while idle; shutdown must not wait for the polling interval")
		}
		if _, err := os.Stat(heartbeat); os.IsNotExist(err) != tt.wantHeartbeatMissing {
			t.Fatalf("Stopped worker must remove its heartbeat, got %v", err)
		}
		probe := exec.Command(binary, "healthcheck")
		probe.Env = process.Env
		if err := probe.Run(); (err != nil) != tt.wantStoppedProbeError {
			t.Fatal("A stopped worker must not pass its health check")
		}
		if !bytes.Contains(output.Bytes(), []byte(tt.wantStartupOutput)) {
			t.Fatalf("Startup must explicitly identify the idle accounting scaffold:\n%s", &output)
		}
	})
	t.Run("worker process rejects invalid config", func(t *testing.T) {
		tt := struct {
			invalidSetting       string
			wantExitError        bool
			wantOutput           string
			wantHeartbeatMissing bool
		}{
			invalidSetting:       "E2B_WORKER_POLL_INTERVAL=invalid",
			wantExitError:        true,
			wantOutput:           "E2B_WORKER_POLL_INTERVAL",
			wantHeartbeatMissing: true,
		}

		heartbeat := filepath.Join(t.TempDir(), "heartbeat")
		process := exec.Command(workerBinary(t))
		process.Env = append(os.Environ(),
			tt.invalidSetting,
			"E2B_WORKER_HEARTBEAT_FILE="+heartbeat,
		)
		output, err := process.CombinedOutput()
		if (err != nil) != tt.wantExitError {
			t.Fatal("Invalid configuration must fail startup")
		}
		if !bytes.Contains(output, []byte(tt.wantOutput)) {
			t.Fatalf("Configuration failure must name the invalid setting: %s", output)
		}
		if _, err := os.Stat(heartbeat); os.IsNotExist(err) != tt.wantHeartbeatMissing {
			t.Fatalf("Invalid startup must not publish a heartbeat, got %v", err)
		}
	})
}

// workerBinary locates the executable built into the repository's Go image;
// missing binaries fail these process tests rather than silently skipping them.
func workerBinary(t *testing.T) string {
	t.Helper()
	binary, err := exec.LookPath("billing-worker")
	if err != nil {
		t.Fatalf("Find billing-worker; run this suite through make test: %v", err)
	}
	return binary
}

// waitForHealthyWorker probes startup within a bounded deadline so the signal
// test begins after the actual runtime loop has published its first heartbeat.
func waitForHealthyWorker(t *testing.T, binary string, env []string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastError error
	for time.Now().Before(deadline) {
		probe := exec.Command(binary, "healthcheck")
		probe.Env = env
		output, err := probe.CombinedOutput()
		if err == nil {
			return
		}
		lastError = fmt.Errorf("%w: %s", err, output)
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Worker did not become healthy: %v", lastError)
}
