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

// Run preserves released measurements through retries, cancellation, barriers,
// lost responses, and receipt failures while respecting both API batch limits.
func TestRun(t *testing.T) {
	t.Run("simulator lost response recovery", func(t *testing.T) {
		tt := struct {
			loseResponse             bool
			maxAttempts              int
			resumeBatchSize          int
			wantStep                 int
			wantPendingAfterLoss     int
			wantLostResponseInjected bool
			wantAttemptsAfterLoss    uint64
			wantPendingAfterResume   int
			wantAttemptsAfterResume  uint64
			wantStored               int
			wantRequests             int
		}{
			loseResponse:             true,
			maxAttempts:              1,
			resumeBatchSize:          1,
			wantStep:                 1,
			wantPendingAfterLoss:     2,
			wantLostResponseInjected: true,
			wantAttemptsAfterLoss:    1,
			wantPendingAfterResume:   0,
			wantAttemptsAfterResume:  3,
			wantStored:               2,
			wantRequests:             3,
		}

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
		sender.LoseResponse, sender.MaxAttempts = tt.loseResponse, tt.maxAttempts
		options := Options{StatePath: path, Action: "run", Mode: "step", Plan: &plan, Sender: sender}
		if err := Run(context.Background(), options); err == nil {
			t.Fatal("Deliberate lost response unexpectedly saved a receipt")
		}

		state := readSimulatorState(t, path)
		if state.NextStep != tt.wantStep || len(state.pending(false)) != tt.wantPendingAfterLoss || state.LostResponseInjected != tt.wantLostResponseInjected || state.Attempts != tt.wantAttemptsAfterLoss {
			t.Fatalf("Lost response checkpoint = %+v", state)
		}

		sender.BatchSize = tt.resumeBatchSize
		options.Action, options.Plan = "send", nil
		if err := Run(context.Background(), options); err != nil {
			t.Fatal(err)
		}

		state = readSimulatorState(t, path)
		if state.NextStep != tt.wantStep || len(state.pending(false)) != tt.wantPendingAfterResume || state.Attempts != tt.wantAttemptsAfterResume {
			t.Fatalf("Resumed checkpoint = %+v", state)
		}

		mutex.Lock()
		defer mutex.Unlock()
		if len(stored) != tt.wantStored || requests != tt.wantRequests {
			t.Errorf("Recovery stored %d identities through %d requests", len(stored), requests)
		}
	})
	t.Run("simulator steps barrier and replay", func(t *testing.T) {
		tt := struct {
			replayBatchSize int
			reverseReplay   bool
			wantSteps       []int
			wantReceived    int
			wantOriginals   int
		}{
			replayBatchSize: 1,
			reverseReplay:   true,
			wantSteps:       []int{1, 2, 2},
			wantReceived:    12,
			wantOriginals:   6,
		}

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
		for invocation, wantStep := range tt.wantSteps {
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
		options.Sender.Reverse, options.Sender.BatchSize = tt.reverseReplay, tt.replayBatchSize
		if err := Run(context.Background(), options); err != nil {
			t.Fatal(err)
		}

		mutex.Lock()
		defer mutex.Unlock()
		if len(received) != tt.wantReceived {
			t.Fatalf("Received %d measurements, want six originals and six retries", len(received))
		}

		for index := range tt.wantOriginals {
			if !sameJSON(received[index], received[tt.wantReceived-1-index]) {
				t.Errorf("Reversed retry changed event %d", index)
			}
		}
	})
	t.Run("simulator retry after retains content", func(t *testing.T) {
		tt := struct {
			retryAfter          string
			firstResponseStatus int
			wantMinimumDelay    time.Duration
			wantRequests        int
			wantBodyUnchanged   bool
			wantPending         int
		}{
			retryAfter:          "2",
			firstResponseStatus: 503,
			wantMinimumDelay:    2 * time.Second,
			wantRequests:        2,
			wantBodyUnchanged:   true,
			wantPending:         0,
		}

		var mutex sync.Mutex
		var bodies [][]byte
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			mutex.Lock()
			bodies = append(bodies, body)
			first := len(bodies) == 1
			mutex.Unlock()
			if first {
				w.Header().Set("Retry-After", tt.retryAfter)
				w.WriteHeader(tt.firstResponseStatus)

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
		if paused < tt.wantMinimumDelay || len(bodies) != tt.wantRequests || bytes.Equal(bodies[0], bodies[1]) != tt.wantBodyUnchanged || len(readSimulatorState(t, path).pending(false)) != tt.wantPending {
			t.Fatalf("Unsafe retry: paused=%s, requests=%d", paused, len(bodies))
		}
	})
	t.Run("simulator permanent errors retain buffer", func(t *testing.T) {
		for _, response := range []struct {
			status         int
			body           string
			wantError      bool
			wantRequests   int32
			wantPending    int
			wantSavedError bool
		}{
			{
				status:         409,
				body:           `{"error":{"code":"event_conflict"}}`,
				wantError:      true,
				wantRequests:   1,
				wantPending:    2,
				wantSavedError: true,
			},
			{
				status:         422,
				body:           `{}`,
				wantError:      true,
				wantRequests:   1,
				wantPending:    2,
				wantSavedError: true,
			},
			{
				status:         302,
				body:           `{}`,
				wantError:      true,
				wantRequests:   1,
				wantPending:    2,
				wantSavedError: true,
			},
			{
				status:         202,
				body:           `{"status":"wrong"}`,
				wantError:      true,
				wantRequests:   1,
				wantPending:    2,
				wantSavedError: true,
			},
			{
				status:         202,
				body:           `{"status":"accepted"} {}`,
				wantError:      true,
				wantRequests:   1,
				wantPending:    2,
				wantSavedError: true,
			},
		} {
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
				if (err != nil) != response.wantError {
					t.Fatalf("Run error = %v; want error=%t", err, response.wantError)
				}

				if requests.Load() != response.wantRequests || len(state.pending(false)) != response.wantPending || (state.LastError != "") != response.wantSavedError {
					t.Fatalf("Rejected response lost pending evidence: requests=%d, err=%v, state=%+v", requests.Load(), err, state)
				}
			})
		}
	})
	t.Run("simulator cancellation retains pending", func(t *testing.T) {
		tt := struct {
			retryAfter     string
			responseStatus int
			wantError      error
			wantPending    int
		}{
			retryAfter:     "60",
			responseStatus: 503,
			wantError:      context.Canceled,
			wantPending:    2,
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", tt.retryAfter)
			w.WriteHeader(tt.responseStatus)
			cancel()
		}))
		defer server.Close()
		sender := simulatorSender(server.URL)
		sender.wait = nil
		plan := simulatorPlan(t)
		path := filepath.Join(t.TempDir(), "run.json")
		err := Run(ctx, Options{StatePath: path, Action: "run", Mode: "step", Plan: &plan, Sender: sender})
		if !errors.Is(err, tt.wantError) || len(readSimulatorState(t, path).pending(false)) != tt.wantPending {
			t.Fatalf("Canceled delivery lost data: %v", err)
		}
	})
	t.Run("simulator receipt write failure recovers", func(t *testing.T) {
		tt := struct {
			wantErrorContains         string
			wantFailureOutput         string
			wantPendingAfterFailure   int
			wantRequestsAfterRecovery int32
			wantStepAfterRecovery     int
			wantPendingAfterRecovery  int
		}{
			wantErrorContains:         "save delivery receipt",
			wantFailureOutput:         "pending=2 delivered=0",
			wantPendingAfterFailure:   2,
			wantRequestsAfterRecovery: 2,
			wantStepAfterRecovery:     1,
			wantPendingAfterRecovery:  0,
		}

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
		if err := Run(context.Background(), options); err == nil || !strings.Contains(err.Error(), tt.wantErrorContains) {
			t.Fatalf("Missing receipt write was not reported: %v", err)
		}

		if !strings.Contains(output.String(), tt.wantFailureOutput) {
			t.Errorf("Failed receipt was displayed as confirmed: %s", output.String())
		}

		if err := os.Rename(directory+"-offline", directory); err != nil {
			t.Fatal(err)
		}

		if len(readSimulatorState(t, path).pending(false)) != tt.wantPendingAfterFailure {
			t.Fatal("Failed receipt write advanced the durable checkpoint")
		}

		if err := Run(context.Background(), options); err != nil {
			t.Fatal(err)
		}

		state := readSimulatorState(t, path)
		if requests.Load() != tt.wantRequestsAfterRecovery || state.NextStep != tt.wantStepAfterRecovery || len(state.pending(false)) != tt.wantPendingAfterRecovery {
			t.Errorf("Receipt recovery advanced the scenario or lost work: %+v", state)
		}
	})
	t.Run("simulator both batch limits", func(t *testing.T) {
		tt := struct {
			identifierBytes             int
			eventIDPrefixBytes          int
			eventCount                  int
			batchSize                   int
			wantMaximumBodyBytes        int
			wantMaximumEventsPerRequest int
			wantDeliveredCount          int
			wantMinimumRequests         int
			wantPending                 int
		}{
			identifierBytes:             256,
			eventIDPrefixBytes:          240,
			eventCount:                  1_000,
			batchSize:                   1_000,
			wantMaximumBodyBytes:        1 << 20,
			wantMaximumEventsPerRequest: 1_000,
			wantDeliveredCount:          1_000,
			wantMinimumRequests:         2,
			wantPending:                 0,
		}

		plan := simulatorPlan(t)
		base := plan.events()[0]
		base.Source, base.CustomerID, base.SandboxID, base.Metric = strings.Repeat("s", tt.identifierBytes), strings.Repeat("c", tt.identifierBytes), strings.Repeat("b", tt.identifierBytes), strings.Repeat("m", tt.identifierBytes)
		plan = Plan{Name: "wide", Steps: []Step{{Name: "wide"}}}
		for index := range tt.eventCount {
			event := base
			event.EventID = strings.Repeat("e", tt.eventIDPrefixBytes) + fmt.Sprintf("%016d", index)
			plan.Steps[0].Events = append(plan.Steps[0].Events, event)
		}

		var mutex sync.Mutex
		count, requests := 0, 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			data, _ := io.ReadAll(r.Body)
			var body struct{ Events []Event }
			if len(data) > tt.wantMaximumBodyBytes || json.Unmarshal(data, &body) != nil || len(body.Events) > tt.wantMaximumEventsPerRequest {
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
		sender.BatchSize = tt.batchSize
		path := filepath.Join(t.TempDir(), "run.json")
		if err := Run(context.Background(), Options{StatePath: path, Action: "run", Mode: "fast", Plan: &plan, Sender: sender}); err != nil {
			t.Fatal(err)
		}

		mutex.Lock()
		defer mutex.Unlock()
		if count != tt.wantDeliveredCount || requests < tt.wantMinimumRequests || len(readSimulatorState(t, path).pending(false)) != tt.wantPending {
			t.Errorf("Byte-limited delivery = %d events, %d requests", count, requests)
		}
	})
	t.Run("simulator corrupt state is retained", func(t *testing.T) {
		tests := []struct {
			name              string
			savedData         string
			wantError         bool
			wantDataUnchanged bool
		}{
			{
				name:              "truncated snapshot",
				savedData:         `{"version":`,
				wantError:         true,
				wantDataUnchanged: true,
			},
			{
				name:              "unsupported version",
				savedData:         `{"version":2}`,
				wantError:         true,
				wantDataUnchanged: true,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "run.json")
				if err := os.WriteFile(path, []byte(tt.savedData), 0600); err != nil {
					t.Fatal(err)
				}

				plan := simulatorPlan(t)
				err := Run(context.Background(), Options{StatePath: path, Action: "generate", Mode: "fast", Plan: &plan})
				if (err != nil) != tt.wantError {
					t.Error("Corrupt state was replaced")
				}

				after, err := os.ReadFile(path)
				if err != nil || (string(after) == tt.savedData) != tt.wantDataUnchanged {
					t.Fatalf("Original state was not retained: %s, %v", after, err)
				}
			})
		}
	})
	t.Run("simulator status during delivery", func(t *testing.T) {
		tt := struct {
			action             string
			wantStateUnchanged bool
			wantOutput         string
		}{
			action:             "status",
			wantStateUnchanged: true,
			wantOutput:         "generated=2 pending=2 delivered=0",
		}

		path := filepath.Join(t.TempDir(), "run.json")
		store, err := OpenStore(path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		state := &State{Version: 1, Plan: simulatorPlan(t), NextStep: 1, Delivered: make([]bool, 6)}
		if err := store.Save(state); err != nil {
			t.Fatal(err)
		}

		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		var output bytes.Buffer
		if err := Run(context.Background(), Options{StatePath: path, Action: tt.action, Mode: "fast", Output: &output}); err != nil {
			t.Fatal(err)
		}

		after, err := os.ReadFile(path)
		if err != nil || bytes.Equal(before, after) != tt.wantStateUnchanged || !strings.Contains(output.String(), tt.wantOutput) {
			t.Fatalf("Concurrent status changed or misreported the buffer: %s, %v", output.String(), err)
		}
	})
	t.Run("simulator changed plan cannot replace pending", func(t *testing.T) {
		tt := struct {
			newSource          string
			wantError          bool
			wantStateUnchanged bool
		}{
			newSource:          "changed-source",
			wantError:          true,
			wantStateUnchanged: true,
		}

		path := filepath.Join(t.TempDir(), "run.json")
		plan := simulatorPlan(t)
		options := Options{StatePath: path, Action: "generate", Mode: "step", Plan: &plan}
		if err := Run(context.Background(), options); err != nil {
			t.Fatal(err)
		}

		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		changed, err := Assignment(tt.newSource, 1, time.Hour)
		if err != nil {
			t.Fatal(err)
		}

		options.Plan = &changed
		if err := Run(context.Background(), options); (err != nil) != tt.wantError {
			t.Error("Changed plan replaced pending work")
		}

		after, err := os.ReadFile(path)
		if err != nil || bytes.Equal(before, after) != tt.wantStateUnchanged {
			t.Error("Changed input mutated the original state")
		}
	})
	t.Run("simulator advance passes only one barrier", func(t *testing.T) {
		tt := struct {
			firstBarrier               string
			advance                    bool
			wantStepAfterFirstCommand  int
			wantStepAfterSecondCommand int
		}{
			firstBarrier:               "First operator pause.",
			advance:                    true,
			wantStepAfterFirstCommand:  2,
			wantStepAfterSecondCommand: 4,
		}

		plan := simulatorPlan(t)
		plan.Steps[1].Barrier = tt.firstBarrier
		path := filepath.Join(t.TempDir(), "run.json")
		options := Options{StatePath: path, Action: "generate", Mode: "fast", Advance: tt.advance, Plan: &plan}
		if err := Run(context.Background(), options); err != nil {
			t.Fatal(err)
		}

		if cursor := readSimulatorState(t, path).NextStep; cursor != tt.wantStepAfterFirstCommand {
			t.Fatalf("One advance passed multiple barriers: cursor=%d", cursor)
		}

		if err := Run(context.Background(), options); err != nil {
			t.Fatal(err)
		}

		if cursor := readSimulatorState(t, path).NextStep; cursor != tt.wantStepAfterSecondCommand {
			t.Errorf("Separate advance did not release the next phase: cursor=%d", cursor)
		}
	})
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

// readSimulatorState inspects the on-disk checkpoint independently of the
// in-memory sender, detecting missing durable release or receipt writes.
func readSimulatorState(t *testing.T, path string) *State {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}

	if err := state.validate(); err != nil {
		t.Fatal(err)
	}

	return &state
}
