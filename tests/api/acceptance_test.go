//go:build integration

package api_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/usage"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestUsageBatchesDurableAcceptance checks the running HTTP service against a
// separate PostgreSQL connection: every measurement and receipt must already
// be committed when 202 arrives, with explicit zero/max units and pending state.
func TestUsageBatchesDurableAcceptance(t *testing.T) {
	pool, source := apiFixture(t)
	events := apiEvents(source)
	events[0].Units, events[0].SchemaVersion = 1<<63-1, 1<<31-1
	before := time.Now().UTC().Truncate(time.Microsecond)
	requireAccepted(t, postUsage(t, batchJSON(t, "first-batch", events)))
	after := time.Now().UTC()
	var firstReceipt time.Time
	for _, event := range events {
		var stored usage.Event
		var receipt time.Time
		var pending, errorFree bool
		err := pool.QueryRow(context.Background(), `
			SELECT source, event_id, schema_version, customer_id, sandbox_id, metric,
				period_start, period_end, units, received_at,
				processed_at IS NULL, processing_error IS NULL
			FROM usage_inbox WHERE source = $1 AND event_id = $2`, source, event.EventID).Scan(
			&stored.Source, &stored.EventID, &stored.SchemaVersion, &stored.CustomerID,
			&stored.SandboxID, &stored.Metric, &stored.PeriodStart, &stored.PeriodEnd,
			&stored.Units, &receipt, &pending, &errorFree,
		)
		if err != nil {
			t.Fatalf("Read committed HTTP event: %v", err)
		}
		if stored.Source != event.Source || stored.EventID != event.EventID ||
			stored.SchemaVersion != event.SchemaVersion || stored.CustomerID != event.CustomerID ||
			stored.SandboxID != event.SandboxID || stored.Metric != event.Metric || stored.Units != event.Units ||
			!stored.PeriodStart.Equal(event.PeriodStart) || !stored.PeriodEnd.Equal(event.PeriodEnd) {
			t.Errorf("HTTP measurement differs: got %+v, want %+v", stored, event)
		}
		if receipt.Before(before) || receipt.After(after) || receipt.Nanosecond()%1_000 != 0 || !pending || !errorFree {
			t.Errorf("Receipt/state = %s, pending %v, error-free %v", receipt, pending, errorFree)
		}
		if firstReceipt.IsZero() {
			firstReceipt = receipt
		} else if !receipt.Equal(firstReceipt) {
			t.Error("One batch supplied different receipt times")
		}
	}
	if count := apiEventCount(t, pool, source); count != 2 {
		t.Errorf("Committed event count = %d, want 2", count)
	}
}

// TestUsageBatchesRetryAndAtomicConflict repeats measurements under a different
// batch identifier/order/offset after accounting state changes. Identical retries
// preserve every column; a later changed event must return 409 and roll back
// an earlier new row, retaining the original measurement and processing state.
func TestUsageBatchesRetryAndAtomicConflict(t *testing.T) {
	pool, source := apiFixture(t)
	events := apiEvents(source)
	requireAccepted(t, postUsage(t, batchJSON(t, "original", events)))
	if _, err := pool.Exec(context.Background(), `
		UPDATE usage_inbox SET processed_at = received_at + interval '1 second'
		WHERE source = $1 AND event_id = 'first'`, source); err != nil {
		t.Fatalf("Set completed accounting fixture: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		UPDATE usage_inbox SET processing_error = 'fixture accounting error'
		WHERE source = $1 AND event_id = 'second'`, source); err != nil {
		t.Fatalf("Set unresolved accounting fixture: %v", err)
	}
	before := apiSnapshot(t, pool, source)
	events[0].PeriodStart = events[0].PeriodStart.In(time.FixedZone("UTC-07", -7*60*60))
	events[0].PeriodEnd = events[0].PeriodEnd.In(time.FixedZone("UTC+08", 8*60*60))
	retry := []usage.Event{events[1], events[0], events[0]}
	requireAccepted(t, postUsage(t, batchJSON(t, "regrouped-retry", retry)))
	if after := apiSnapshot(t, pool, source); after != before {
		t.Errorf("Retry changed stored rows: before %s, after %s", before, after)
	}
	newEvent := events[0]
	newEvent.EventID = "000-new-before-conflict"
	changed := events[0]
	changed.Units++
	requireHTTPError(t, postUsage(t, batchJSON(t, "conflict", []usage.Event{newEvent, changed})), 409, "event_conflict", "", source, changed.EventID)
	if after := apiSnapshot(t, pool, source); after != before {
		t.Errorf("Conflicting batch changed original rows: %s", after)
	}
	invalid := events[1]
	invalid.Units = -1
	requireHTTPError(t, postUsage(t, batchJSON(t, "invalid", []usage.Event{newEvent, invalid})), 422, "invalid_batch", "events[1].units", "", "")
	if count := apiEventCount(t, pool, source); count != 2 {
		t.Errorf("Rejected batch left %d events, want 2 original rows", count)
	}
}

// TestUsageBatchesConflictingIncomingDuplicates reuses a new identity with two
// different values in one request. It must return 409 without persisting either
// version or another otherwise valid measurement from that batch.
func TestUsageBatchesConflictingIncomingDuplicates(t *testing.T) {
	pool, source := apiFixture(t)
	events := apiEvents(source)
	changed := events[0]
	changed.Units++
	requireHTTPError(t, postUsage(t, batchJSON(t, "incoming-conflict", append(events, changed))), 409, "event_conflict", "", source, changed.EventID)
	if count := apiEventCount(t, pool, source); count != 0 {
		t.Errorf("Incoming conflict persisted %d events", count)
	}
}

// TestUsageBatchesConcurrentRetries sends eight identical HTTP requests together.
// Every caller must receive the accepted contract while PostgreSQL stores each
// event once; this includes the network/server/repository path under contention.
func TestUsageBatchesConcurrentRetries(t *testing.T) {
	pool, source := apiFixture(t)
	body := batchJSON(t, "concurrent", apiEvents(source))
	const clients = 8
	start := make(chan struct{})
	results := make(chan error, clients)
	for range clients {
		go func() {
			<-start
			request, err := http.NewRequest(http.MethodPost, apiURL()+"/usage/batches", strings.NewReader(body))
			if err != nil {
				results <- err
				return
			}
			request.Header.Set("Content-Type", "application/json")
			response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
			if err == nil {
				defer response.Body.Close()
				var result struct{ Status string }
				err = json.NewDecoder(response.Body).Decode(&result)
				if err == nil && (response.StatusCode != 202 || result.Status != "accepted") {
					err = fmt.Errorf("HTTP response = %d %q, want 202 accepted", response.StatusCode, result.Status)
				}
			}
			results <- err
		}()
	}
	close(start)
	for range clients {
		if err := <-results; err != nil {
			t.Errorf("Concurrent HTTP retry: %v", err)
		}
	}
	if count := apiEventCount(t, pool, source); count != 2 {
		t.Errorf("Concurrent HTTP event count = %d, want 2", count)
	}
}

// TestUsageBatchesMaximumBatch sends 1_000 independently keyed measurements
// through the actual HTTP/database path. The inclusive event limit must commit
// all rows with one receipt time, rather than merely passing parser validation.
func TestUsageBatchesMaximumBatch(t *testing.T) {
	pool, source := apiFixture(t)
	base := apiEvents(source)[0]
	events := make([]usage.Event, 1_000)
	for index := range events {
		events[index] = base
		events[index].EventID = fmt.Sprintf("event-%04d", index)
		events[index].Units = int64(index)
	}
	requireAccepted(t, postUsage(t, batchJSON(t, "maximum-batch", events)))
	var count, receipts int
	var units int64
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*), count(DISTINCT received_at), sum(units)::bigint
		FROM usage_inbox WHERE source = $1`, source).Scan(&count, &receipts, &units); err != nil {
		t.Fatalf("Verify maximum committed batch: %v", err)
	}
	if count != 1_000 || receipts != 1 || units != 999*1_000/2 {
		t.Errorf("Maximum batch = %d rows, %d receipt times, %d units", count, receipts, units)
	}
}

// apiURL selects the running service's test address and removes a trailing slash
// so the same integration tests can run on Compose or a local Go installation.
func apiURL() string {
	if address := os.Getenv("E2B_API_URL"); address != "" {
		return strings.TrimRight(address, "/")
	}
	return "http://127.0.0.1:8081"
}

// openAPIDatabase opens a separately connected UTC pool for committed-state
// assertions; it never imports the HTTP handler or bypasses the running server.
func openAPIDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := inbox.OpenPool(ctx, os.Getenv("E2B_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("Open API verification database (set PG variables or E2B_TEST_DATABASE_URL): %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// apiFixture creates a unique producer namespace and removes only its own rows
// after the test, preserving unrelated development data and previous fixtures.
func apiFixture(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	pool := openAPIDatabase(t)
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatalf("Generate test producer namespace: %v", err)
	}
	source := "http-test-" + hex.EncodeToString(random[:])
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DELETE FROM usage_inbox WHERE source = $1", source); err != nil {
			t.Errorf("Remove owned HTTP fixtures: %v", err)
		}
	})
	return pool, source
}

// preserveHappyPathFixture retains any pre-existing original contract row and
// cleans up a newly created one after the fixed HTTP happy-path request.
func preserveHappyPathFixture(t *testing.T) {
	t.Helper()
	pool := openAPIDatabase(t)
	var existed bool
	if err := pool.QueryRow(context.Background(), `
		SELECT EXISTS (SELECT 1 FROM usage_inbox
			WHERE source = 'api-contract-test' AND event_id = 'acme-cpu-2026-10-10-12')`).Scan(&existed); err != nil {
		t.Fatalf("Inspect fixed happy-path fixture: %v", err)
	}
	if !existed {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := pool.Exec(ctx, `
				DELETE FROM usage_inbox
				WHERE source = 'api-contract-test' AND event_id = 'acme-cpu-2026-10-10-12'`); err != nil {
				t.Errorf("Remove new happy-path fixture: %v", err)
			}
		})
	}
}

// apiEvents supplies two measurements including zero units, microsecond
// precision, and a UTC month boundary expressed with a non-UTC offset.
func apiEvents(source string) []usage.Event {
	first := usage.Event{
		Source: source, EventID: "first", SchemaVersion: 1, CustomerID: "acme",
		SandboxID: "sandbox-001", Metric: "cpu_seconds", Units: 100_000_000,
		PeriodStart: time.Date(2_026, 10, 31, 23, 59, 59, 999_999_000, time.UTC).In(time.FixedZone("UTC+08", 8*60*60)),
		PeriodEnd:   time.Date(2_026, 11, 1, 0, 0, 0, 0, time.UTC),
	}
	second := first
	second.EventID, second.Units = "second", 0
	return []usage.Event{first, second}
}

// batchJSON serializes domain fixtures as explicit transport fields while
// retaining integer precision and each timestamp's selected offset.
func batchJSON(t *testing.T, batchID string, events []usage.Event) string {
	t.Helper()
	items := make([]map[string]any, 0, len(events))
	for _, event := range events {
		items = append(items, map[string]any{
			"source": event.Source, "event_id": event.EventID, "schema_version": event.SchemaVersion,
			"customer_id": event.CustomerID, "sandbox_id": event.SandboxID, "metric": event.Metric,
			"period_start": event.PeriodStart.Format(time.RFC3339Nano),
			"period_end":   event.PeriodEnd.Format(time.RFC3339Nano), "units": event.Units,
		})
	}
	body, err := json.Marshal(map[string]any{"batch_id": batchID, "events": items})
	if err != nil {
		t.Fatalf("Encode explicit HTTP fixture: %v", err)
	}
	return string(body)
}

// postUsage submits a JSON batch to the running API with a timeout longer than
// its transaction deadline; response assertions own closing the returned body.
func postUsage(t *testing.T, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, apiURL()+"/usage/batches", strings.NewReader(body))
	if err != nil {
		t.Fatalf("Create HTTP fixture request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("Send HTTP fixture: %v", err)
	}
	return response
}

// requireAccepted checks the public successful response before subsequent
// database assertions prove that acceptance follows a visible commit.
func requireAccepted(t *testing.T, response *http.Response) {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("Read acceptance: %v", err)
	}
	var accepted struct{ Status string }
	if err := json.Unmarshal(body, &accepted); err != nil || response.StatusCode != 202 ||
		response.Header.Get("Content-Type") != "application/json" || accepted.Status != "accepted" {
		t.Fatalf("Acceptance = %d %s, decoding error %v", response.StatusCode, body, err)
	}
}

// requireHTTPError verifies rejected requests expose the documented code and
// optional field or conflict identity, with no successful acknowledgement.
func requireHTTPError(t *testing.T, response *http.Response, status int, code, field, source, eventID string) {
	t.Helper()
	defer response.Body.Close()
	var body struct {
		Error struct {
			Code, Message, Field, Source string
			EventID                      string `json:"event_id"`
		}
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("Decode HTTP rejection: %v", err)
	}
	if response.StatusCode != status || response.Header.Get("Content-Type") != "application/json" ||
		body.Error.Code != code || body.Error.Message == "" || body.Error.Field != field ||
		body.Error.Source != source || body.Error.EventID != eventID {
		t.Fatalf("HTTP rejection = %d %+v, want %d %s", response.StatusCode, body.Error, status, code)
	}
}

// apiEventCount counts only the uniquely owned namespace through an independent
// database connection, proving committed visibility without unrelated rows.
func apiEventCount(t *testing.T, pool *pgxpool.Pool, source string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM usage_inbox WHERE source = $1", source).Scan(&count); err != nil {
		t.Fatalf("Count HTTP fixture events: %v", err)
	}
	return count
}

// apiSnapshot compares every column of the owned rows before and after retries
// or conflicts, detecting changes to receipt times and accounting metadata.
func apiSnapshot(t *testing.T, pool *pgxpool.Pool, source string) string {
	t.Helper()
	var snapshot string
	if err := pool.QueryRow(context.Background(), `
		SELECT jsonb_agg(to_jsonb(usage_inbox) ORDER BY event_id)::text
		FROM usage_inbox WHERE source = $1`, source).Scan(&snapshot); err != nil {
		t.Fatalf("Snapshot HTTP fixture rows: %v", err)
	}
	return snapshot
}
