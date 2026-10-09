//go:build integration

package inbox_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/usage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestInboxInsertBatch verifies actual PostgreSQL storage of every measurement
// field, integer bounds, pending state, and the explicitly supplied receipt
// instant. Offset input must preserve its UTC instant at microsecond precision.
func TestInboxInsertBatch(t *testing.T) {
	pool := testDatabase(t, nil)
	store := inbox.NewPostgres(pool)
	events := fixtureEvents()
	maximum := events[0]
	maximum.EventID = "maximum-values"
	maximum.SchemaVersion, maximum.Units = 1<<31-1, 1<<63-1
	events = append(events, maximum)
	receipt := time.Date(2026, 11, 1, 8, 30, 5, 123456789, time.FixedZone("UTC+08", 8*60*60))
	if err := store.InsertBatch(context.Background(), events, receipt); err != nil {
		t.Fatalf("Insert batch: %v", err)
	}
	if count := eventCount(t, pool); count != len(events) {
		t.Fatalf("Stored event count = %d, want %d", count, len(events))
	}
	for _, event := range events {
		var stored usage.Event
		var received time.Time
		var pending, errorFree bool
		err := pool.QueryRow(context.Background(), `
			SELECT source, event_id, schema_version, customer_id, sandbox_id, metric,
				period_start, period_end, units, received_at,
				processed_at IS NULL, processing_error IS NULL
			FROM usage_inbox WHERE source = $1 AND event_id = $2`, event.Source, event.EventID).Scan(
			&stored.Source, &stored.EventID, &stored.SchemaVersion, &stored.CustomerID,
			&stored.SandboxID, &stored.Metric, &stored.PeriodStart, &stored.PeriodEnd,
			&stored.Units, &received, &pending, &errorFree,
		)
		if err != nil {
			t.Fatalf("Read stored event: %v", err)
		}
		if stored.Source != event.Source || stored.EventID != event.EventID ||
			stored.SchemaVersion != event.SchemaVersion || stored.CustomerID != event.CustomerID ||
			stored.SandboxID != event.SandboxID || stored.Metric != event.Metric || stored.Units != event.Units ||
			!stored.PeriodStart.Equal(event.PeriodStart) || !stored.PeriodEnd.Equal(event.PeriodEnd) {
			t.Errorf("Stored measurement differs: got %+v, want %+v", stored, event)
		}
		if !received.Equal(receipt.UTC().Truncate(time.Microsecond)) || !pending || !errorFree {
			t.Errorf("Stored receipt/state = %s, pending %v, error-free %v", received, pending, errorFree)
		}
	}
}

// TestInboxConflictRollback exercises a late content conflict after a new
// event was inserted. The batch must roll back its new row and preserve the
// earlier committed measurement and receipt time.
func TestInboxConflictRollback(t *testing.T) {
	pool := testDatabase(t, nil)
	store := inbox.NewPostgres(pool)
	events := fixtureEvents()
	receipt := time.Date(2026, 11, 1, 0, 30, 5, 0, time.UTC)
	if err := store.InsertBatch(context.Background(), events[:1], receipt); err != nil {
		t.Fatalf("Seed existing event: %v", err)
	}
	changed := events[0]
	changed.Units++
	events[1].EventID = "000-new-before-conflict"
	err := store.InsertBatch(context.Background(), []usage.Event{events[1], changed}, receipt.Add(time.Minute))
	assertConflict(t, err, changed)
	if count := eventCount(t, pool); count != 1 {
		t.Errorf("Event count after rollback = %d, want 1", count)
	}
	var units int64
	var received time.Time
	if err := pool.QueryRow(context.Background(), "SELECT units, received_at FROM usage_inbox").Scan(&units, &received); err != nil {
		t.Fatalf("Read original event: %v", err)
	}
	if units != events[0].Units || !received.Equal(receipt) {
		t.Error("Failed batch changed the original measurement or receipt time")
	}
}

// assertConflict verifies typed content conflicts identify the original event,
// rather than exposing a generic primary-key error or silently replacing it.
func assertConflict(t *testing.T, err error, event usage.Event) {
	t.Helper()
	var conflict *inbox.ConflictError
	if !errors.As(err, &conflict) || conflict.Source != event.Source || conflict.EventID != event.EventID {
		t.Fatalf("Conflict = %v, want identity (%q, %q)", err, event.Source, event.EventID)
	}
}

// TestInboxDatabaseFailureRollback forces a database-only CHECK failure on the
// second event. This verifies transaction rollback after an actual insert,
// independently of application validation or future duplicate handling.
func TestInboxDatabaseFailureRollback(t *testing.T) {
	pool := testDatabase(t, nil)
	if _, err := pool.Exec(context.Background(), "ALTER TABLE usage_inbox ADD CHECK (event_id <> 'reject-last')"); err != nil {
		t.Fatalf("Set temporary database constraint: %v", err)
	}
	events := fixtureEvents()
	events[1].EventID = "reject-last"
	err := inbox.NewPostgres(pool).InsertBatch(context.Background(), events, time.Now().UTC())
	assertPostgresError(t, err, "23514")
	if count := eventCount(t, pool); count != 0 {
		t.Errorf("Partial failed batch persisted %d events", count)
	}
}

// TestInboxInvalidValues requires explicit receipt time and validates all input
// before committing anything. Empty batches, invalid receipts, and an invalid
// second event must leave the private inbox empty.
func TestInboxInvalidValues(t *testing.T) {
	pool := testDatabase(t, nil)
	store := inbox.NewPostgres(pool)
	events := fixtureEvents()
	invalid := append([]usage.Event(nil), events...)
	invalid[1].Units = -1
	for _, tt := range []struct {
		name    string
		events  []usage.Event
		receipt time.Time
	}{
		{"empty batch", nil, time.Now().UTC()},
		{"missing receipt", events, time.Time{}},
		{"invalid receipt year", events, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"invalid second event", invalid, time.Now().UTC()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := store.InsertBatch(context.Background(), tt.events, tt.receipt); err == nil {
				t.Fatal("Invalid batch was accepted")
			}
			if count := eventCount(t, pool); count != 0 {
				t.Errorf("Invalid batch persisted %d events", count)
			}
		})
	}
}

// TestInboxCancelledContext verifies that a canceled request is not committed
// and does not poison the connection pool for the next valid batch.
func TestInboxCancelledContext(t *testing.T) {
	pool := testDatabase(t, nil)
	store := inbox.NewPostgres(pool)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := store.InsertBatch(ctx, fixtureEvents(), time.Now().UTC())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Canceled insert error = %v, want context.Canceled", err)
	}
	if count := eventCount(t, pool); count != 0 {
		t.Errorf("Canceled batch persisted %d events", count)
	}
	if err := store.InsertBatch(context.Background(), fixtureEvents(), time.Now().UTC()); err != nil {
		t.Fatalf("Pool unusable after canceled request: %v", err)
	}
}

// TestInboxSynchronousCommit starts with asynchronous sessions and adds a CHECK
// that only permits writes with synchronous_commit on. The store must override
// that setting for its transaction and restore the caller's session afterwards.
func TestInboxSynchronousCommit(t *testing.T) {
	pool := testDatabase(t, map[string]string{"synchronous_commit": "off"})
	if _, err := pool.Exec(context.Background(), "ALTER TABLE usage_inbox ADD CHECK (current_setting('synchronous_commit') = 'on')"); err != nil {
		t.Fatalf("Set durability verification constraint: %v", err)
	}
	if err := inbox.NewPostgres(pool).InsertBatch(context.Background(), fixtureEvents(), time.Now().UTC()); err != nil {
		t.Fatalf("Synchronous inbox commit: %v", err)
	}
	var setting string
	if err := pool.QueryRow(context.Background(), "SHOW synchronous_commit").Scan(&setting); err != nil {
		t.Fatalf("Read session setting: %v", err)
	}
	if setting != "off" {
		t.Errorf("Session setting leaked outside transaction: %q", setting)
	}
}

// testDatabase applies the tracked initial migration in a uniquely owned schema
// and returns a pool that isolates each test or benchmark from application data.
// Cleanup closes its connections before dropping only that schema; optional
// settings let tests exercise transaction behavior under different sessions.
func testDatabase(t testing.TB, settings map[string]string) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := inbox.OpenPool(ctx, os.Getenv("E2B_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("Open test database (set PG variables or E2B_TEST_DATABASE_URL): %v", err)
	}
	t.Cleanup(admin.Close)
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatalf("Generate private schema name: %v", err)
	}
	schemaName := "test_inbox_" + hex.EncodeToString(random[:])
	schema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("Create private schema: %v", err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("Remove private schema: %v", err)
		}
	})
	config := admin.Config()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.ConnConfig.RuntimeParams["application_name"] = schemaName
	for key, value := range settings {
		config.ConnConfig.RuntimeParams[key] = value
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("Create private-schema pool: %v", err)
	}
	t.Cleanup(pool.Close)
	migration, err := os.ReadFile("../../migrations/001_usage_inbox.sql")
	if err != nil {
		t.Fatalf("Read initial migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatalf("Apply initial migration to private schema: %v", err)
	}
	var zone string
	if err := pool.QueryRow(ctx, "SHOW timezone").Scan(&zone); err != nil || zone != "UTC" {
		t.Fatalf("Inbox session timezone = %q, error %v; want UTC", zone, err)
	}
	return pool
}

// fixtureEvents supplies two valid, independently keyed measurements, including
// explicit zero units and a consumption interval crossing the UTC month boundary.
func fixtureEvents() []usage.Event {
	first := usage.Event{
		Source: "inbox-test", EventID: "first", SchemaVersion: 1, CustomerID: "acme",
		SandboxID: "sandbox-001", Metric: "cpu_seconds", Units: 100000000,
		PeriodStart: time.Date(2026, 10, 31, 23, 30, 0, 123456000, time.UTC),
		PeriodEnd:   time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
	}
	second := first
	second.EventID, second.CustomerID, second.Units = "second", "cyberdyne", 0
	second.PeriodStart = first.PeriodStart.In(time.FixedZone("UTC+08", 8*60*60))
	return []usage.Event{first, second}
}

// eventCount checks committed visibility through a separate pooled query, so
// rollback tests cannot accidentally inspect uncommitted transaction contents.
func eventCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM usage_inbox").Scan(&count); err != nil {
		t.Fatalf("Count inbox events: %v", err)
	}
	return count
}

// assertPostgresError checks the actual SQLSTATE behind wrapped errors to ensure
// rollback scenarios reached the intended database constraint rather than merely
// failing earlier in input validation or connection setup.
func assertPostgresError(t *testing.T, err error, code string) {
	t.Helper()
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) || pgError.Code != code {
		t.Fatalf("Database error = %v, want SQLSTATE %s", err, code)
	}
}
