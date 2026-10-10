package simulator

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSenderValidate checks complete transport configurations at inclusive
// limits, preserving zero delay and unlimited attempts while rejecting zero
// request deadlines, invalid backoff, and excess duplicate copies.
func TestSenderValidate(t *testing.T) {
	cases := []struct {
		name                      string
		clientTimeout             time.Duration
		batchSize                 int
		delay, retryMin, retryMax time.Duration
		attempts, duplicates      int
		wantError                 bool
		wantMessage               string
	}{
		{name: "zero request timeout", clientTimeout: 0, batchSize: 1, delay: 0, retryMin: time.Second, retryMax: time.Second, attempts: 0, duplicates: 0, wantError: true, wantMessage: "an HTTP client with a positive timeout is required"},
		{name: "zero retry minimum", clientTimeout: time.Second, batchSize: 1, delay: 0, retryMin: 0, retryMax: time.Second, attempts: 0, duplicates: 0, wantError: true, wantMessage: "delay must be non-negative and retry delays must satisfy 0 < retry-min <= retry-max <= 1h"},
		{name: "equal retry bounds", clientTimeout: time.Second, batchSize: 1, delay: 0, retryMin: time.Second, retryMax: time.Second, attempts: 0, duplicates: 0},
		{name: "maximum retry hour", clientTimeout: time.Second, batchSize: 1, delay: 0, retryMin: time.Second, retryMax: time.Hour, attempts: 0, duplicates: 0},
		{name: "beyond retry hour", clientTimeout: time.Second, batchSize: 1, delay: 0, retryMin: time.Second, retryMax: time.Hour + time.Nanosecond, attempts: 0, duplicates: 0, wantError: true, wantMessage: "delay must be non-negative and retry delays must satisfy 0 < retry-min <= retry-max <= 1h"},
		{name: "maximum duplicates", clientTimeout: time.Second, batchSize: 1000, delay: 0, retryMin: time.Second, retryMax: time.Second, attempts: 0, duplicates: 10},
		{name: "excess duplicates", clientTimeout: time.Second, batchSize: 1000, delay: 0, retryMin: time.Second, retryMax: time.Second, attempts: 0, duplicates: 11, wantError: true, wantMessage: "max-attempts must be non-negative and duplicates must be between 0 and 10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sender := Sender{BaseURL: "http://127.0.0.1:8081", Client: &http.Client{Timeout: tc.clientTimeout}, BatchSize: tc.batchSize, BatchDelay: tc.delay, RetryMin: tc.retryMin, RetryMax: tc.retryMax, MaxAttempts: tc.attempts, Duplicates: tc.duplicates}
			err := sender.validate()
			if (err != nil) != tc.wantError {
				t.Fatalf("Sender.validate(%+v): error=%v; wantError=%t", tc, err, tc.wantError)
			}

			if tc.wantError && err.Error() != tc.wantMessage {
				t.Errorf("Sender.validate(%+v): error=%q; want %q", tc, err, tc.wantMessage)
			}
		})
	}
}

// TestClassifyDeliveryResponse distinguishes the first server error from an
// ordinary client error and rejects non-JSON receipts even with accepted content.
func TestClassifyDeliveryResponse(t *testing.T) {
	cases := []struct {
		name                 string
		status               int
		contentType, body    string
		wantRetry, wantError bool
		wantMessage          string
	}{
		{name: "accepted JSON", status: 202, contentType: "application/json", body: `{"status":"accepted"}`},
		{name: "accepted non-JSON", status: 202, contentType: "text/plain", body: `{"status":"accepted"}`, wantError: true, wantMessage: "invalid acknowledgement"},
		{name: "below server error boundary", status: 499, contentType: "application/json", body: `{}`, wantError: true, wantMessage: "requires investigation"},
		{name: "first server error", status: 500, contentType: "application/json", body: `{}`, wantRetry: true, wantError: true, wantMessage: "HTTP 500"},
		{name: "next server error", status: 501, contentType: "application/json", body: `{}`, wantRetry: true, wantError: true, wantMessage: "HTTP 501"},
		{name: "rate limited", status: 429, contentType: "application/json", body: `{}`, wantRetry: true, wantError: true, wantMessage: "HTTP 429"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": []string{tc.contentType}}}
			got := classifyDeliveryResponse(response, []byte(tc.body), nil)
			if (got.Err != nil) != tc.wantError {
				t.Fatalf("classifyDeliveryResponse(status=%d type=%q body=%s): error=%v; wantError=%t", tc.status, tc.contentType, tc.body, got.Err, tc.wantError)
			}

			if tc.wantError && !strings.Contains(got.Err.Error(), tc.wantMessage) {
				t.Errorf("classifyDeliveryResponse(status=%d): error=%q; want containing %q", tc.status, got.Err, tc.wantMessage)
			}

			if got.Retry != tc.wantRetry {
				t.Errorf("classifyDeliveryResponse(status=%d): retry=%t; want %t", tc.status, got.Retry, tc.wantRetry)
			}
		})
	}
}

// TestValidateWorkflowTransport verifies retry boundaries independently of an
// actual workflow: zero means unlimited attempts, and equal positive delays work.
func TestValidateWorkflowTransport(t *testing.T) {
	cases := []struct {
		name               string
		retryMin, retryMax time.Duration
		attempts           int
		wantError          bool
		wantMessage        string
	}{
		{name: "zero retry minimum", retryMin: 0, retryMax: time.Second, attempts: 0, wantError: true, wantMessage: "workflow requires a client and valid retry settings"},
		{name: "equal retry delays", retryMin: time.Second, retryMax: time.Second, attempts: 1},
		{name: "unlimited attempts", retryMin: time.Millisecond, retryMax: time.Second, attempts: 0},
		{name: "negative attempts", retryMin: time.Millisecond, retryMax: time.Second, attempts: -1, wantError: true, wantMessage: "workflow requires a client and valid retry settings"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			options := WorkflowOptions{BaseURL: "http://127.0.0.1:8081", Client: &http.Client{Timeout: time.Second}, RetryMin: tc.retryMin, RetryMax: tc.retryMax, MaxAttempts: tc.attempts}
			err := validateWorkflowTransport(options)
			if (err != nil) != tc.wantError {
				t.Fatalf("validateWorkflowTransport(%+v): error=%v; wantError=%t", tc, err, tc.wantError)
			}

			if tc.wantError && err.Error() != tc.wantMessage {
				t.Errorf("validateWorkflowTransport(%+v): error=%q; want %q", tc, err, tc.wantMessage)
			}
		})
	}
}

// TestValidateWorkflowStep checks the supported HTTP status interval without
// issuing requests; both endpoints are valid and adjacent outliers are rejected.
func TestValidateWorkflowStep(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		wantError   bool
		wantMessage string
	}{
		{name: "below status interval", status: 99, wantError: true, wantMessage: "step probe: invalid relative path or expected status"},
		{name: "lowest status", status: 100},
		{name: "highest status", status: 599},
		{name: "above status interval", status: 600, wantError: true, wantMessage: "step probe: invalid relative path or expected status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			step := WorkflowStep{Name: "probe", Request: WorkflowRequest{Method: "GET", Path: "/healthz"}, Want: WorkflowExpectation{Status: tc.status}}
			err := validateWorkflowStep(step)
			if (err != nil) != tc.wantError {
				t.Fatalf("validateWorkflowStep(status=%d): error=%v; wantError=%t", tc.status, err, tc.wantError)
			}

			if tc.wantError && err.Error() != tc.wantMessage {
				t.Errorf("validateWorkflowStep(status=%d): error=%q; want %q", tc.status, err, tc.wantMessage)
			}
		})
	}
}

// TestLoadWorkflow preserves the exact four-MiB limit. Padding a valid small
// document with JSON whitespace isolates file size from schema correctness.
func TestLoadWorkflow(t *testing.T) {
	cases := []struct {
		name        string
		inputBytes  int
		wantName    string
		wantSteps   int
		wantError   bool
		wantMessage string
	}{
		{name: "last allowed file byte", inputBytes: 4_194_304, wantName: "boundary", wantSteps: 1},
		{name: "first excess file byte", inputBytes: 4_194_305, wantError: true, wantMessage: "workflow exceeds four MiB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := `{"name":"boundary","steps":[{"name":"health","request":{"method":"GET","path":"/healthz"},"want":{"status":200}}]}`
			input += strings.Repeat(" ", tc.inputBytes-len(input))
			path := filepath.Join(t.TempDir(), "workflow.json")
			if err := os.WriteFile(path, []byte(input), 0600); err != nil {
				t.Fatalf("write workflow fixture (%d bytes): %v", tc.inputBytes, err)
			}

			got, err := LoadWorkflow(path)
			if (err != nil) != tc.wantError {
				t.Fatalf("LoadWorkflow(%d bytes): error=%v; wantError=%t", tc.inputBytes, err, tc.wantError)
			}

			if tc.wantError {
				if err.Error() != tc.wantMessage {
					t.Errorf("LoadWorkflow(%d bytes): error=%q; want %q", tc.inputBytes, err, tc.wantMessage)
				}

				return
			}

			if got.Name != tc.wantName || len(got.Steps) != tc.wantSteps {
				t.Errorf("LoadWorkflow(%d bytes): name=%q steps=%d; want %q %d", tc.inputBytes, got.Name, len(got.Steps), tc.wantName, tc.wantSteps)
			}
		})
	}
}

// TestWorkflowRunnerRequest checks response byte boundaries with a real HTTP
// server and a read-only step, so rejecting a large body cannot create charges.
func TestWorkflowRunnerRequest(t *testing.T) {
	cases := []struct {
		name                  string
		inputBytes            int
		wantBytes, wantStatus int
		wantError             bool
		wantMessage           string
	}{
		{name: "last allowed response byte", inputBytes: 1_048_576, wantBytes: 1_048_576, wantStatus: 200},
		{name: "first excess response byte", inputBytes: 1_048_577, wantError: true, wantMessage: "workflow response exceeds one MiB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := bytes.Repeat([]byte(" "), tc.inputBytes)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(input) }))
			defer server.Close()
			runner := workflowRunner{options: WorkflowOptions{Source: "response-boundary", BaseURL: server.URL, Client: server.Client()}}
			step := WorkflowStep{Name: "health", Request: WorkflowRequest{Method: "GET", Path: "/healthz"}}

			got, err := runner.request(context.Background(), step)
			if (err != nil) != tc.wantError {
				t.Fatalf("workflow request(%d bytes): error=%v; wantError=%t", tc.inputBytes, err, tc.wantError)
			}

			if tc.wantError {
				if err.Error() != tc.wantMessage {
					t.Errorf("workflow request(%d bytes): error=%q; want %q", tc.inputBytes, err, tc.wantMessage)
				}

				return
			}

			if len(got.body) != tc.wantBytes || got.status != tc.wantStatus {
				t.Errorf("workflow request(%d bytes): bytes=%d status=%d; want %d %d", tc.inputBytes, len(got.body), got.status, tc.wantBytes, tc.wantStatus)
			}
		})
	}
}

// TestPlanValidate checks unique event budgets directly, using only valid
// explicit events; the exact limit succeeds and one additional event fails.
func TestPlanValidate(t *testing.T) {
	cases := []struct {
		name        string
		eventCount  int
		wantError   bool
		wantMessage string
	}{
		{name: "exact event budget", eventCount: 10_000},
		{name: "one beyond event budget", eventCount: 10_001, wantError: true, wantMessage: "the scenario must contain at most 10000 events"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := make([]Event, tc.eventCount)
			for index := range events {
				events[index] = Event{Source: "event-budget", EventID: fmt.Sprintf("event-%d", index), SchemaVersion: 1, CustomerID: "acme", SandboxID: "sandbox", Metric: "cpu_seconds", PeriodStart: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC), PeriodEnd: time.Date(2026, 10, 10, 13, 0, 0, 0, time.UTC), Units: 1}
			}

			plan := Plan{Name: "budget", Steps: []Step{{Name: "all", Events: events}}}

			err := plan.validate()
			if (err != nil) != tc.wantError {
				t.Fatalf("Plan.validate(events=%d): error=%v; wantError=%t", tc.eventCount, err, tc.wantError)
			}

			if tc.wantError && err.Error() != tc.wantMessage {
				t.Errorf("Plan.validate(events=%d): error=%q; want %q", tc.eventCount, err, tc.wantMessage)
			}
		})
	}
}

// TestSenderDeliver verifies the requested number of duplicate transmissions,
// including one extra copy; every copy retains the same immutable JSON bytes.
func TestSenderDeliver(t *testing.T) {
	cases := []struct {
		name         string
		duplicates   int
		wantCalls    int
		wantAttempts uint64
		wantError    bool
	}{
		{name: "no duplicate", duplicates: 0, wantCalls: 1, wantAttempts: 1},
		{name: "one extra copy", duplicates: 1, wantCalls: 2, wantAttempts: 2},
		{name: "maximum extra copies", duplicates: 10, wantCalls: 11, wantAttempts: 11},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inputBody := []byte(`{"events":[]}`)
			calls := make(chan struct{}, 11)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read duplicate transmission: %v", err)
				}

				if !bytes.Equal(got, inputBody) {
					t.Errorf("duplicate transmission body=%s; want %s", got, inputBody)
				}

				calls <- struct{}{}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(202)
				_, _ = io.WriteString(w, `{"status":"accepted"}`)
			}))
			defer server.Close()
			store, err := OpenStore(filepath.Join(t.TempDir(), "sender.json"))
			if err != nil {
				t.Fatalf("open duplicate checkpoint: %v", err)
			}
			defer store.Close()
			state := &State{Version: 1, Plan: simulatorPlan(t), NextStep: 1, Delivered: make([]bool, 6)}
			sender := simulatorSender(server.URL)
			sender.Duplicates = tc.duplicates

			err = sender.deliver(context.Background(), store, state, inputBody, 0)
			if (err != nil) != tc.wantError {
				t.Fatalf("deliver(duplicates=%d): error=%v; wantError=%t", tc.duplicates, err, tc.wantError)
			}

			if len(calls) != tc.wantCalls || state.Attempts != tc.wantAttempts {
				t.Errorf("deliver(duplicates=%d): calls=%d attempts=%d; want %d %d", tc.duplicates, len(calls), state.Attempts, tc.wantCalls, tc.wantAttempts)
			}
		})
	}
}

// TestNextBatch uses valid bounded identifiers to reach the exact HTTP byte
// limit. The last event fits at equality, while one additional source byte must
// leave that event pending without altering the preceding encoded measurements.
func TestNextBatch(t *testing.T) {
	cases := []struct {
		name                                                        string
		fullEvents, fullIdentifierBytes, lastSourceBytes, batchSize int
		wantEvents, wantBodyBytes                                   int
		wantError                                                   bool
	}{
		{name: "one byte below body limit", fullEvents: 720, fullIdentifierBytes: 256, lastSourceBytes: 63, batchSize: 721, wantEvents: 721, wantBodyBytes: 1_048_575},
		{name: "exact body limit", fullEvents: 720, fullIdentifierBytes: 256, lastSourceBytes: 64, batchSize: 721, wantEvents: 721, wantBodyBytes: 1_048_576},
		{name: "one byte above body limit splits batch", fullEvents: 720, fullIdentifierBytes: 256, lastSourceBytes: 65, batchSize: 721, wantEvents: 720, wantBodyBytes: 1_048_332},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := make([]Event, 0, tc.fullEvents+1)
			for index := range tc.fullEvents {
				events = append(events, Event{
					Source: strings.Repeat("x", tc.fullIdentifierBytes), EventID: fmt.Sprintf("%0*d", tc.fullIdentifierBytes, index), SchemaVersion: 1,
					CustomerID: strings.Repeat("x", tc.fullIdentifierBytes), SandboxID: strings.Repeat("x", tc.fullIdentifierBytes), Metric: strings.Repeat("x", tc.fullIdentifierBytes),
					PeriodStart: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC), PeriodEnd: time.Date(2026, 10, 10, 13, 0, 0, 0, time.UTC), Units: 1,
				})
			}

			events = append(events, Event{Source: strings.Repeat("x", tc.lastSourceBytes), EventID: "x", SchemaVersion: 1, CustomerID: "x", SandboxID: "x", Metric: "x", PeriodStart: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC), PeriodEnd: time.Date(2026, 10, 10, 13, 0, 0, 0, time.UTC), Units: 1})
			indices := make([]int, len(events))
			for index := range indices {
				indices[index] = index
			}

			gotIndices, gotBody, err := nextBatch(events, indices, tc.batchSize)
			if (err != nil) != tc.wantError {
				t.Fatalf("nextBatch(lastSourceBytes=%d): error=%v; wantError=%t", tc.lastSourceBytes, err, tc.wantError)
			}

			if len(gotIndices) != tc.wantEvents || len(gotBody) != tc.wantBodyBytes {
				t.Errorf("nextBatch(lastSourceBytes=%d): events=%d bytes=%d; want %d %d", tc.lastSourceBytes, len(gotIndices), len(gotBody), tc.wantEvents, tc.wantBodyBytes)
			}
		})
	}
}

// TestWorkflowBackoff saturates at the integer half-cap boundary, including
// odd nanosecond caps, and avoids overflow near the duration maximum.
func TestWorkflowBackoff(t *testing.T) {
	cases := []struct {
		name                      string
		delay, maximum, wantDelay time.Duration
	}{
		{name: "below odd half cap", delay: 1, maximum: 5, wantDelay: 2},
		{name: "at odd half cap", delay: 2, maximum: 5, wantDelay: 5},
		{name: "at even half cap", delay: 3, maximum: 6, wantDelay: 6},
		{name: "large delay saturates without overflow", delay: 9_223_372_036_854_775_806, maximum: 9_223_372_036_854_775_807, wantDelay: 9_223_372_036_854_775_807},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := workflowBackoff(tc.delay, tc.maximum)
			if got != tc.wantDelay {
				t.Errorf("workflowBackoff(delay=%s, maximum=%s)=%s; want %s", tc.delay, tc.maximum, got, tc.wantDelay)
			}
		})
	}
}
