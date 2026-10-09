package simulator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSimulatorLostResponseRecovery receives a successful commit response but
// deliberately loses it before recording a receipt. A separately reopened run
// must resend the original content, even when batch boundaries change.
func TestSimulatorLostResponseRecovery(t *testing.T) {
	var mutex sync.Mutex
	stored := make(map[string]Event)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Events []Event }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mutex.Lock()
		requests++
		for _, event := range body.Events {
			if previous, present := stored[event.EventID]; present && !sameJSON(previous, event) {
				t.Error("Retry changed measurement content")
			}
			stored[event.EventID] = event
		}
		mutex.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		io.WriteString(w, `{"status":"accepted"}`)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "run.json")
	plan := simulatorPlan(t)
	sender := simulatorSender(server.URL)
	sender.LoseResponse, sender.MaxAttempts = true, 1
	options := Options{StatePath: path, Action: "run", Mode: "step", Plan: &plan, Sender: sender}
	if err := Run(context.Background(), options); err == nil {
		t.Fatal("Deliberate lost response unexpectedly saved a receipt")
	}
	state := readSimulatorState(t, path)
	if state.NextStep != 1 || len(state.pending(false)) != 2 || !state.LostResponseInjected || state.Attempts != 1 {
		t.Fatalf("Lost response checkpoint = %+v", state)
	}
	sender.BatchSize = 1
	options.Action, options.Plan = "send", nil
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	state = readSimulatorState(t, path)
	if state.NextStep != 1 || len(state.pending(false)) != 0 || state.Attempts != 3 {
		t.Fatalf("Resumed checkpoint = %+v", state)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if len(stored) != 2 || requests != 3 {
		t.Errorf("Recovery stored %d identities through %d requests", len(stored), requests)
	}
}

// TestSimulatorStepsBarrierAndReplay verifies the release cursor is durable
// before each request, late October waits for explicit advancement, and reversed
// one-event replay changes delivery order without changing measurement values.
func TestSimulatorStepsBarrierAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	var mutex sync.Mutex
	var received []Event
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := os.ReadFile(path)
		var checkpoint State
		if err != nil || json.Unmarshal(data, &checkpoint) != nil || checkpoint.NextStep == 0 {
			t.Error("Request preceded durable scenario release")
		}
		var body struct{ Events []Event }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mutex.Lock()
		received = append(received, body.Events...)
		mutex.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		io.WriteString(w, `{"status":"accepted"}`)
	}))
	defer server.Close()
	plan := simulatorPlan(t)
	options := Options{StatePath: path, Action: "run", Mode: "step", Plan: &plan, Sender: simulatorSender(server.URL)}
	for invocation, wantStep := range []int{1, 2, 2} {
		if err := Run(context.Background(), options); err != nil {
			t.Fatal(err)
		}
		if got := readSimulatorState(t, path).NextStep; got != wantStep {
			t.Errorf("Invocation %d: cursor = %d, want %d", invocation, got, wantStep)
		}
	}
	options.Advance, options.Mode = true, "fast"
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	options.Action, options.Plan = "replay", nil
	options.Sender.Reverse, options.Sender.BatchSize = true, 1
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if len(received) != 12 {
		t.Fatalf("Received %d measurements, want six originals and six retries", len(received))
	}
	for index := range 6 {
		if !sameJSON(received[index], received[11-index]) {
			t.Errorf("Reversed retry changed event %d", index)
		}
	}
}

// TestSimulatorRetryAfterRetainsContent retries a server failure using its
// minimum delay and identical bytes, then records a receipt only after 202.
func TestSimulatorRetryAfterRetainsContent(t *testing.T) {
	var mutex sync.Mutex
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mutex.Lock()
		bodies = append(bodies, body)
		first := len(bodies) == 1
		mutex.Unlock()
		if first {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		io.WriteString(w, `{"status":"accepted"}`)
	}))
	defer server.Close()
	sender := simulatorSender(server.URL)
	var paused time.Duration
	sender.wait = func(ctx context.Context, duration time.Duration) error { paused = duration; return ctx.Err() }
	plan := simulatorPlan(t)
	path := filepath.Join(t.TempDir(), "run.json")
	if err := Run(context.Background(), Options{StatePath: path, Action: "run", Mode: "step", Plan: &plan, Sender: sender}); err != nil {
		t.Fatal(err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if paused < 2*time.Second || len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) || len(readSimulatorState(t, path).pending(false)) != 0 {
		t.Fatalf("Unsafe retry: paused=%s, requests=%d", paused, len(bodies))
	}
}

// TestSimulatorPermanentErrorsRetainBuffer rejects conflicts, invalid input,
// redirects, and malformed success acknowledgements without retrying or
// replacing IDs. All released measurements must remain pending for diagnosis.
func TestSimulatorPermanentErrorsRetainBuffer(t *testing.T) {
	for _, response := range []struct {
		status int
		body   string
	}{{409, `{"error":{"code":"event_conflict"}}`}, {422, `{}`}, {302, `{}`}, {202, `{"status":"wrong"}`}, {202, `{"status":"accepted"} {}`}} {
		t.Run(fmt.Sprint(response.status)+response.body, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Location", "/different-endpoint")
				w.WriteHeader(response.status)
				io.WriteString(w, response.body)
			}))
			defer server.Close()
			plan := simulatorPlan(t)
			path := filepath.Join(t.TempDir(), "run.json")
			err := Run(context.Background(), Options{StatePath: path, Action: "run", Mode: "step", Plan: &plan, Sender: simulatorSender(server.URL)})
			state := readSimulatorState(t, path)
			if err == nil || requests.Load() != 1 || len(state.pending(false)) != 2 || state.LastError == "" {
				t.Fatalf("Rejected response lost pending evidence: requests=%d, err=%v, state=%+v", requests.Load(), err, state)
			}
		})
	}
}

// TestSimulatorCancellationRetainsPending cancels during a retry delay. The
// command exits promptly while its already released measurements remain saved.
func TestSimulatorCancellationRetainsPending(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(503)
		cancel()
	}))
	defer server.Close()
	sender := simulatorSender(server.URL)
	sender.wait = nil
	plan := simulatorPlan(t)
	path := filepath.Join(t.TempDir(), "run.json")
	err := Run(ctx, Options{StatePath: path, Action: "run", Mode: "step", Plan: &plan, Sender: sender})
	if !errors.Is(err, context.Canceled) || len(readSimulatorState(t, path).pending(false)) != 2 {
		t.Fatalf("Canceled delivery lost data: %v", err)
	}
}

// TestSimulatorReceiptWriteFailureRecovers simulates storage disappearing
// after HTTP acceptance. Restoring the volume and reopening must replay the
// unrecorded receipt rather than treating that measurement as safely delivered.
func TestSimulatorReceiptWriteFailureRecovers(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "sender")
	path := filepath.Join(directory, "run.json")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			if err := os.Rename(directory, directory+"-offline"); err != nil {
				t.Error(err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		io.WriteString(w, `{"status":"accepted"}`)
	}))
	defer server.Close()
	plan := simulatorPlan(t)
	var output bytes.Buffer
	options := Options{StatePath: path, Action: "run", Mode: "step", Plan: &plan, Sender: simulatorSender(server.URL), Output: &output}
	if err := Run(context.Background(), options); err == nil || !strings.Contains(err.Error(), "save delivery receipt") {
		t.Fatalf("Missing receipt write was not reported: %v", err)
	}
	if !strings.Contains(output.String(), "pending=2 delivered=0") {
		t.Errorf("Failed receipt was displayed as confirmed: %s", output.String())
	}
	if err := os.Rename(directory+"-offline", directory); err != nil {
		t.Fatal(err)
	}
	if len(readSimulatorState(t, path).pending(false)) != 2 {
		t.Fatal("Failed receipt write advanced the durable checkpoint")
	}
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	state := readSimulatorState(t, path)
	if requests.Load() != 2 || state.NextStep != 1 || len(state.pending(false)) != 0 {
		t.Errorf("Receipt recovery advanced the scenario or lost work: %+v", state)
	}
}

// TestSimulatorBothBatchLimits sends wide identifiers that exceed 1 MiB at
// 1000 events. Transport must split by bytes as well as count without omission.
func TestSimulatorBothBatchLimits(t *testing.T) {
	plan := simulatorPlan(t)
	base := plan.events()[0]
	base.Source, base.CustomerID, base.SandboxID, base.Metric = strings.Repeat("s", 256), strings.Repeat("c", 256), strings.Repeat("b", 256), strings.Repeat("m", 256)
	plan = Plan{Name: "wide", Steps: []Step{{Name: "wide"}}}
	for index := range 1000 {
		event := base
		event.EventID = strings.Repeat("e", 240) + fmt.Sprintf("%016d", index)
		plan.Steps[0].Events = append(plan.Steps[0].Events, event)
	}
	var mutex sync.Mutex
	count, requests := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body struct{ Events []Event }
		if len(data) > maxBatchBytes || json.Unmarshal(data, &body) != nil || len(body.Events) > 1000 {
			t.Error("Request violated an API batch limit")
		}
		mutex.Lock()
		count += len(body.Events)
		requests++
		mutex.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		io.WriteString(w, `{"status":"accepted"}`)
	}))
	defer server.Close()
	sender := simulatorSender(server.URL)
	sender.BatchSize = 1000
	path := filepath.Join(t.TempDir(), "run.json")
	if err := Run(context.Background(), Options{StatePath: path, Action: "run", Mode: "fast", Plan: &plan, Sender: sender}); err != nil {
		t.Fatal(err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if count != 1000 || requests < 2 || len(readSimulatorState(t, path).pending(false)) != 0 {
		t.Errorf("Byte-limited delivery = %d events, %d requests", count, requests)
	}
}

// simulatorSender removes real waiting from retry tests while keeping real
// HTTP requests, explicit timeouts, and the same persistent delivery protocol.
func simulatorSender(address string) *Sender {
	return &Sender{
		BaseURL: address, Client: &http.Client{Timeout: 5 * time.Second},
		BatchSize: 100, RetryMin: time.Millisecond, RetryMax: time.Second,
		wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	}
}
