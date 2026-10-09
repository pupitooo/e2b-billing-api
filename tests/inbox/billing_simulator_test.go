//go:build integration

package inbox_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/httpapi"
	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/worker"
)

// TestBillingSimulatorExecutable runs each readable JSON workflow through a
// separate CLI, real HTTP router, worker, and private seeded PostgreSQL schema.
// The files keep every public input beside its literal invoice/credit expectation.
func TestBillingSimulatorExecutable(t *testing.T) {
	cases := []struct {
		name                string
		file                string
		unavailableRequests int32
		wantCompleted       int
	}{
		{name: "complete assignment", file: "billing-assignment", wantCompleted: 24},
		{name: "command identities and changed-content conflicts", file: "billing-command-retries", wantCompleted: 16},
		{name: "exact sub-cent credit", file: "billing-exact-credit", wantCompleted: 6},
		{name: "credit exhaustion and a full recurring add-on", file: "billing-credit-exhaustion", wantCompleted: 9},
		{name: "platform reads and resets monthly limit status", file: "billing-limit-status", wantCompleted: 15},
		{name: "default versions and customer override", file: "billing-price-versions", wantCompleted: 8},
		{name: "ambiguous price interval blocks closing", file: "billing-price-boundary", wantCompleted: 5},
		{name: "ambiguous month interval blocks closing", file: "billing-month-boundary", wantCompleted: 5},
		{name: "HTTP outage recovers without financial drift", file: "billing-assignment", unavailableRequests: 2, wantCompleted: 24},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			address := billingSimulatorAPI(t, scenario.unavailableRequests)
			binary := billingSimulatorBinary(t)
			state := filepath.Join(t.TempDir(), "workflow.json")
			arguments := billingSimulatorArguments(scenario.file, state, address)
			output := runBillingSimulator(t, binary, false, arguments...)
			assertWorkflowCheckpoint(t, state, scenario.wantCompleted, "")
			if !strings.Contains(output, "completed=") {
				t.Fatalf("workflow %s: missing completion status; output=%s", scenario.file, output)
			}
		})
	}
	t.Run("new producer process confirms a lost committed grant", func(t *testing.T) {
		scenario := struct {
			file               string
			firstArguments     []string
			wantFirstError     bool
			wantFirstCompleted int
			wantFinalCompleted int
			wantFirstOutput    string
		}{file: "billing-assignment", firstArguments: []string{"--max-attempts=1"}, wantFirstError: true, wantFirstCompleted: 2, wantFinalCompleted: 24, wantFirstOutput: "injected lost committed response"}
		address := billingSimulatorAPI(t, 0)
		binary := billingSimulatorBinary(t)
		state := filepath.Join(t.TempDir(), "workflow.json")
		base := billingSimulatorArguments(scenario.file, state, address)
		output := runBillingSimulator(t, binary, scenario.wantFirstError, append(append([]string(nil), base...), scenario.firstArguments...)...)
		if !strings.Contains(output, scenario.wantFirstOutput) {
			t.Fatalf("first producer output=%s want fragment=%s", output, scenario.wantFirstOutput)
		}
		assertWorkflowCheckpoint(t, state, scenario.wantFirstCompleted, scenario.wantFirstOutput)
		runBillingSimulator(t, binary, false, base...)
		assertWorkflowCheckpoint(t, state, scenario.wantFinalCompleted, "")
		before, err := os.ReadFile(state)
		if err != nil {
			t.Fatal(err)
		}
		runBillingSimulator(t, binary, false, append(base, "--action=status")...)
		runBillingSimulator(t, binary, false, base...)
		after, err := os.ReadFile(state)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatal("status or completed restart changed the durable workflow checkpoint")
		}
	})
	t.Run("unavailable API retains the plan for a new process", func(t *testing.T) {
		scenario := struct {
			file               string
			unavailableURL     string
			wantFirstError     bool
			wantFirstCompleted int
			wantFinalCompleted int
		}{file: "billing-exact-credit", unavailableURL: "http://127.0.0.1:1", wantFirstError: true, wantFirstCompleted: 0, wantFinalCompleted: 6}
		address := billingSimulatorAPI(t, 0)
		binary := billingSimulatorBinary(t)
		state := filepath.Join(t.TempDir(), "workflow.json")
		failed := append(billingSimulatorArguments(scenario.file, state, scenario.unavailableURL), "--max-attempts=1")
		runBillingSimulator(t, binary, scenario.wantFirstError, failed...)
		assertWorkflowCheckpoint(t, state, scenario.wantFirstCompleted, "connection refused")
		runBillingSimulator(t, binary, false, billingSimulatorArguments(scenario.file, state, address)...)
		assertWorkflowCheckpoint(t, state, scenario.wantFinalCompleted, "")
	})
}

// billingSimulatorAPI provides a real seeded account and worker isolated from
// application data. Optional gateway failures happen before any command executes.
func billingSimulatorAPI(t *testing.T, unavailableRequests int32) string {
	t.Helper()
	pool := billingDatabase(t)
	store := billing.NewStore(pool)
	handler := httpapi.NewHandler(inbox.NewPostgres(pool, time.Second), 5*time.Second, 8, store)
	var remaining atomic.Int32
	remaining.Store(unavailableRequests)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if remaining.Add(-1) >= 0 {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"error":{"code":"billing_unavailable"}}`)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	w := worker.Worker{ProcessBatch: store.ProcessBatch, PollInterval: time.Millisecond, BatchTimeout: 5 * time.Second,
		HeartbeatFile: filepath.Join(t.TempDir(), "heartbeat"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("workflow worker shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("workflow worker did not stop before schema cleanup")
		}
	})
	return server.URL
}

// billingSimulatorBinary selects the built CLI or builds one for a local test;
// no financial fixture values are supplied by this transport helper.
func billingSimulatorBinary(t *testing.T) string {
	t.Helper()
	if binary := os.Getenv("E2B_SIMULATOR_BINARY"); binary != "" {
		return binary
	}
	if binary, err := exec.LookPath("platform-simulator"); err == nil {
		return binary
	}
	binary := filepath.Join(t.TempDir(), "platform-simulator")
	if output, err := exec.Command("go", "build", "-o", binary, "../../cmd/platform-simulator").CombinedOutput(); err != nil {
		t.Fatalf("build billing simulator: %v\n%s", err, output)
	}
	return binary
}

// billingSimulatorArguments identifies the readable scenario file and durable
// namespace, with bounded test retries instead of implicit financial defaults.
func billingSimulatorArguments(file, state, address string) []string {
	return []string{"--scenario=" + file, "--file=../../docs/simulator/" + file + ".json", "--state=" + state,
		"--api-url=" + address, "--source=billing-workflow", "--retry-min=1ms", "--retry-max=5ms", "--max-attempts=2000"}
}

// runBillingSimulator checks the declared process outcome before exposing its
// output. Cancellation bounds retries even if a regression never reaches a step.
func runBillingSimulator(t *testing.T, binary string, wantError bool, arguments ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, arguments...).CombinedOutput()
	if ctx.Err() != nil || (err != nil) != wantError {
		t.Fatalf("billing simulator %v: error=%v wantError=%t context=%v\n%s", arguments, err, wantError, ctx.Err(), output)
	}
	return string(output)
}

// assertWorkflowCheckpoint compares the durable cursor and error with explicit
// expectations; API invoice and credit assertions remain in the scenario JSON.
func assertWorkflowCheckpoint(t *testing.T, path string, wantCompleted int, wantErrorFragment string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint struct {
		NextStep  int    `json:"next_step"`
		LastError string `json:"last_error"`
	}
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.NextStep != wantCompleted || (wantErrorFragment == "" && checkpoint.LastError != "") || !strings.Contains(checkpoint.LastError, wantErrorFragment) {
		t.Fatalf("checkpoint=%+v want completed=%d error fragment=%q", checkpoint, wantCompleted, wantErrorFragment)
	}
}
