package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"e2b/billing-api/internal/httpapi"
	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/usage"
)

type storeFunc func(context.Context, []usage.Event, time.Time) error

// InsertBatch adapts an individual test scenario to the production store
// contract, allowing tests to control commit completion and failures.
func (store storeFunc) InsertBatch(ctx context.Context, events []usage.Event, receipt time.Time) error {
	return store(ctx, events, receipt)
}

// acceptingStore supplies successful storage for transport-only unit tests.
// Durable database behavior is exercised separately against PostgreSQL.
func acceptingStore() storeFunc {
	return func(context.Context, []usage.Event, time.Time) error { return nil }
}

// TestUsageBatchConfiguredDeadline lets storage wait for its request context.
// A short configured deadline must yield retryable 503 instead of retaining the
// old ten-second deadline or writing an acceptance without a commit.
func TestUsageBatchConfiguredDeadline(t *testing.T) {
	store := storeFunc(func(ctx context.Context, _ []usage.Event, _ time.Time) error {
		<-ctx.Done()
		return ctx.Err()
	})
	request := httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(validUsageBatch))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpapi.NewHandler(store, 20*time.Millisecond, 32).ServeHTTP(response, request)
	assertRequestError(t, response, 503, "inbox_unavailable", "")
	if response.Header().Get("Retry-After") != "1" {
		t.Error("Timed-out batch omitted retry guidance")
	}
}

// TestUsageBatchAdmissionBudget holds one admitted batch at storage, saturating
// a one-slot handler. Excess work must receive retryable 503 without reading its
// body, health must remain available, and completion must free the slot.
func TestUsageBatchAdmissionBudget(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	var calls atomic.Int32
	store := storeFunc(func(ctx context.Context, _ []usage.Event, _ time.Time) error {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	handler := httpapi.NewHandler(store, time.Second, 1)
	first := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(validUsageBatch))
	request.Header.Set("Content-Type", "application/json")
	finished := make(chan struct{})
	go func() { handler.ServeHTTP(first, request); close(finished) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("First batch did not reach storage")
	}
	rejected := httptest.NewRecorder()
	reader := &observedBody{}
	handler.ServeHTTP(rejected, httptest.NewRequest(http.MethodPost, "/usage/batches", reader))
	assertRequestError(t, rejected, 503, "inbox_unavailable", "")
	if reader.read || rejected.Header().Get("Retry-After") != "1" || calls.Load() != 1 {
		t.Error("Rejected batch read its body, reached storage, or omitted retry guidance")
	}
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != 200 {
		t.Errorf("Health during overload = %d, want 200", health.Code)
	}
	unblock()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Admitted batch did not complete")
	}
	if first.Code != 202 {
		t.Errorf("Admitted batch = %d, want 202", first.Code)
	}
	response := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(validUsageBatch))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	if response.Code != 202 || calls.Load() != 2 {
		t.Error("Completed batch did not release its admission slot")
	}
}

type observedBody struct{ read bool }

// Read detects any attempt to allocate or decode an overloaded batch's body.
// The admission test expects this reader to remain untouched.
func (body *observedBody) Read([]byte) (int, error) {
	body.read = true
	return 0, errors.New("unexpected body read")
}

// TestUsageBatchStoreInput verifies storage receives fully parsed measurements,
// original identifier text, canonical timestamp instants, an explicit UTC
// receipt at microsecond precision, and a bounded request context.
func TestUsageBatchStoreInput(t *testing.T) {
	body := changeEvent(t, func(e map[string]any) {
		e["source"] = "  platform-test  "
		e["period_start"] = "2026-10-10T20:00:00+08:00"
		e["units"] = 0
	})
	before := time.Now().UTC().Truncate(time.Microsecond)
	calls := 0
	store := storeFunc(func(ctx context.Context, events []usage.Event, receipt time.Time) error {
		calls++
		if len(events) != 1 {
			t.Fatalf("Stored batch length = %d, want 1", len(events))
		}
		event := events[0]
		if event.Source != "  platform-test  " || event.EventID != "test-event" ||
			event.CustomerID != "acme" || event.SandboxID != "sandbox-001" ||
			event.SchemaVersion != 1 || event.Metric != "cpu_seconds" || event.Units != 0 ||
			!event.PeriodStart.Equal(time.Date(2_026, 10, 10, 12, 0, 0, 0, time.UTC)) {
			t.Errorf("Parsed store input = %+v", event)
		}
		if event.PeriodStart.Location() != time.UTC || receipt.Location() != time.UTC ||
			receipt.Before(before) || receipt.After(time.Now()) || receipt.Nanosecond()%1_000 != 0 {
			t.Errorf("Receipt or consumption time was not supplied canonically: %s", receipt)
		}
		deadline, bounded := ctx.Deadline()
		if !bounded || time.Until(deadline) <= 0 || time.Until(deadline) > 10*time.Second {
			t.Errorf("Store context deadline = %s, bounded %v", deadline, bounded)
		}
		return nil
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	httpapi.NewHandler(store, 10*time.Second, 32).ServeHTTP(response, request)
	if response.Code != 202 || calls != 1 {
		t.Errorf("Acceptance status = %d, store calls %d; want 202 and one call", response.Code, calls)
	}
}

// TestUsageBatchValidationBeforeStorage sends a valid first event followed by an
// invalid measurement. Whole-batch validation must reject it without invoking
// storage, so a valid prefix cannot create partial database writes.
func TestUsageBatchValidationBeforeStorage(t *testing.T) {
	body := changeBatch(t, func(batch map[string]any) {
		first := batch["events"].([]any)[0]
		second := map[string]any{}
		for key, value := range first.(map[string]any) {
			second[key] = value
		}
		second["event_id"], second["units"] = "second", -1
		batch["events"] = []any{first, second}
	})
	calls := 0
	store := storeFunc(func(context.Context, []usage.Event, time.Time) error {
		calls++
		return nil
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	httpapi.NewHandler(store, 10*time.Second, 32).ServeHTTP(response, request)
	assertRequestError(t, response, 422, "invalid_batch", "events[1].units")
	if calls != 0 {
		t.Errorf("Invalid batch invoked storage %d times", calls)
	}
}

type observedResponse struct {
	*httptest.ResponseRecorder
	statuses chan int
}

// WriteHeader exposes response timing through a channel so the commit-order
// test can observe headers without racing the recorder's mutable state.
func (response *observedResponse) WriteHeader(status int) {
	response.statuses <- status
	response.ResponseRecorder.WriteHeader(status)
}

// TestUsageBatchWaitsForCommit blocks storage until explicitly released. The
// handler must write no success headers before that point, then return 202 only
// when the store has finished, preserving the durable acknowledgement boundary.
func TestUsageBatchWaitsForCommit(t *testing.T) {
	entered, committed := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	store := storeFunc(func(ctx context.Context, _ []usage.Event, _ time.Time) error {
		close(entered)
		select {
		case <-committed:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	response := &observedResponse{httptest.NewRecorder(), make(chan int, 1)}
	request := httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(validUsageBatch)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() {
		httpapi.NewHandler(store, 10*time.Second, 32).ServeHTTP(response, request)
		close(done)
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("Handler never reached storage")
	}
	select {
	case status := <-response.statuses:
		t.Fatalf("Response %d was sent before commit", status)
	default:
	}
	close(committed)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("Handler did not finish after commit")
	}
	if response.Code != 202 {
		t.Errorf("Committed response = %d, want 202", response.Code)
	}
}

// TestUsageBatchStoreFailures maps a wrapped content conflict to 409 with the
// affected identity. Other storage/commit failures return a generic retryable
// 503, keeping SQL details out of the response and never acknowledging success.
func TestUsageBatchStoreFailures(t *testing.T) {
	for _, tt := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"content conflict", fmt.Errorf("wrapped: %w", &inbox.ConflictError{Source: "platform-test", EventID: "test-event"}), 409, "event_conflict"},
		{"database unavailable", errors.New("secret SQL connection details"), 503, "inbox_unavailable"},
		{"unknown commit outcome", errors.New("commit connection closed"), 503, "inbox_unavailable"},
		{"canceled transaction", context.Canceled, 503, "inbox_unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(validUsageBatch))
			request.Header.Set("Content-Type", "application/json")
			store := storeFunc(func(context.Context, []usage.Event, time.Time) error { return tt.err })
			httpapi.NewHandler(store, 10*time.Second, 32).ServeHTTP(response, request)
			if strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Body.String(), "accepted") {
				t.Errorf("Failure response leaked details or acknowledged: %s", response.Body)
			}
			var body struct {
				Error struct {
					Source  string `json:"source"`
					EventID string `json:"event_id"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("Decode storage failure: %v", err)
			}
			if tt.status == 409 && (body.Error.Source != "platform-test" || body.Error.EventID != "test-event") {
				t.Errorf("Conflict identity = %+v", body.Error)
			}
			if tt.status == 503 && response.Header().Get("Retry-After") != "1" {
				t.Errorf("Retry-After = %q, want 1", response.Header().Get("Retry-After"))
			}
			assertRequestError(t, response, tt.status, tt.code, "")
		})
	}
}

// TestUsageBatchMissingStore ensures an unconfigured handler cannot acknowledge
// valid input, while the health route can still report HTTP server availability.
func TestUsageBatchMissingStore(t *testing.T) {
	handler := httpapi.NewHandler(nil, 10*time.Second, 32)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(validUsageBatch))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	assertRequestError(t, response, 503, "inbox_unavailable", "")
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != 200 || health.Body.Len() != 0 {
		t.Errorf("Health = %d %s, want empty 200", health.Code, health.Body)
	}
}

// TestUsageBatchRequestDeadline lets the caller's shorter deadline cancel a
// blocked store. The handler must return 503 rather than success and must pass
// that cancellation into the transaction work.
func TestUsageBatchRequestDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	store := storeFunc(func(ctx context.Context, _ []usage.Event, _ time.Time) error {
		<-ctx.Done()
		return ctx.Err()
	})
	request := httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(validUsageBatch)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpapi.NewHandler(store, 10*time.Second, 32).ServeHTTP(response, request)
	assertRequestError(t, response, 503, "inbox_unavailable", "")
}
