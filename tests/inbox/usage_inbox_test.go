//go:build integration

package inbox_test

import (
	"context"
	"crypto/rand"
	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/usage"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
	"time"
)

// Postgres.InsertBatch preserves explicit values and durable receipts, rolls
// back failed batches, and resolves identical or conflicting concurrent retries.
func TestPostgresInsertBatch(t *testing.T) {
	t.Run("inbox insert batch", func(t *testing.T) {
		tt := struct {
			events         []usage.Event
			maximumVersion int32
			maximumUnits   int64
			receipt        time.Time
			wantCount      int
			wantReceipt    time.Time
			wantPending    bool
			wantErrorFree  bool
		}{
			events:         fixtureEvents(),
			maximumVersion: 1<<31 - 1,
			maximumUnits:   1<<63 - 1,
			receipt:        time.Date(2026, 11, 1, 8, 30, 5, 123_456_789, time.FixedZone("UTC+08", 8*60*60)),
			wantCount:      3,
			wantReceipt:    time.Date(2026, 11, 1, 0, 30, 5, 123_456_000, time.UTC),
			wantPending:    true,
			wantErrorFree:  true,
		}

		pool := testDatabase(t, nil)
		store := inbox.NewPostgres(pool, 5*time.Second)
		events := tt.events
		maximum := events[0]
		maximum.EventID = "maximum-values"
		maximum.SchemaVersion, maximum.Units = tt.maximumVersion, tt.maximumUnits
		events = append(events, maximum)
		receipt := tt.receipt
		if err := store.InsertBatch(context.Background(), events, receipt); err != nil {
			t.Fatalf("Insert batch: %v", err)
		}
		if count := eventCount(t, pool); count != tt.wantCount {
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
			if !received.Equal(tt.wantReceipt) || pending != tt.wantPending || errorFree != tt.wantErrorFree {
				t.Errorf("Stored receipt/state = %s, pending %v, error-free %v", received, pending, errorFree)
			}
		}
	})
	t.Run("inbox conflict rollback", func(t *testing.T) {
		tt := struct {
			events            []usage.Event
			receipt           time.Time
			changedUnits      int64
			wantCount         int
			wantOriginalUnits int64
		}{
			events:            fixtureEvents(),
			receipt:           time.Date(2026, 11, 1, 0, 30, 5, 0, time.UTC),
			changedUnits:      100_000_001,
			wantCount:         1,
			wantOriginalUnits: 100_000_000,
		}

		pool := testDatabase(t, nil)
		store := inbox.NewPostgres(pool, 5*time.Second)
		events := tt.events
		receipt := tt.receipt
		if err := store.InsertBatch(context.Background(), events[:1], receipt); err != nil {
			t.Fatalf("Seed existing event: %v", err)
		}
		changed := events[0]
		changed.Units = tt.changedUnits
		events[1].EventID = "000-new-before-conflict"
		err := store.InsertBatch(context.Background(), []usage.Event{events[1], changed}, receipt.Add(time.Minute))
		assertConflict(t, err, changed)
		if count := eventCount(t, pool); count != tt.wantCount {
			t.Errorf("Event count after rollback = %d, want 1", count)
		}
		var units int64
		var received time.Time
		if err := pool.QueryRow(context.Background(), "SELECT units, received_at FROM usage_inbox").Scan(&units, &received); err != nil {
			t.Fatalf("Read original event: %v", err)
		}
		if units != tt.wantOriginalUnits || !received.Equal(receipt) {
			t.Error("Failed batch changed the original measurement or receipt time")
		}
	})
	t.Run("inbox database failure rollback", func(t *testing.T) {
		tt := struct {
			constraintSQL   string
			rejectedEventID string
			wantSQLState    string
			wantCount       int
		}{
			constraintSQL:   "ALTER TABLE usage_inbox ADD CHECK (event_id <> 'reject-last')",
			rejectedEventID: "reject-last",
			wantSQLState:    "23514",
			wantCount:       0,
		}

		pool := testDatabase(t, nil)
		if _, err := pool.Exec(context.Background(), tt.constraintSQL); err != nil {
			t.Fatalf("Set temporary database constraint: %v", err)
		}
		events := fixtureEvents()
		events[1].EventID = tt.rejectedEventID
		err := inbox.NewPostgres(pool, 5*time.Second).InsertBatch(context.Background(), events, time.Now().UTC())
		assertPostgresError(t, err, tt.wantSQLState)
		if count := eventCount(t, pool); count != tt.wantCount {
			t.Errorf("Partial failed batch persisted %d events", count)
		}
	})
	t.Run("inbox invalid values", func(t *testing.T) {
		pool := testDatabase(t, nil)
		store := inbox.NewPostgres(pool, 5*time.Second)
		events := fixtureEvents()
		invalid := append([]usage.Event(nil), events...)
		invalid[1].Units = -1
		for _, tt := range []struct {
			name      string
			events    []usage.Event
			receipt   time.Time
			wantError bool
			wantCount int
		}{
			{
				name:      "empty batch",
				events:    nil,
				receipt:   time.Now().UTC(),
				wantError: true,
				wantCount: 0,
			},
			{
				name:      "missing receipt",
				events:    events,
				receipt:   time.Time{},
				wantError: true,
				wantCount: 0,
			},
			{
				name:      "invalid receipt year",
				events:    events,
				receipt:   time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
				wantError: true,
				wantCount: 0,
			},
			{
				name:      "receipt before minimum UTC year",
				events:    events,
				receipt:   time.Date(999, 12, 31, 23, 59, 59, 0, time.UTC),
				wantError: true,
				wantCount: 0,
			},
			{
				name:      "offset moves receipt before minimum UTC year",
				events:    events,
				receipt:   time.Date(1000, 1, 1, 0, 0, 0, 0, time.FixedZone("UTC+01", 60*60)),
				wantError: true,
				wantCount: 0,
			},
			{
				name:      "invalid second event",
				events:    invalid,
				receipt:   time.Now().UTC(),
				wantError: true,
				wantCount: 0,
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				if err := store.InsertBatch(context.Background(), tt.events, tt.receipt); (err != nil) != tt.wantError {
					t.Fatal("Invalid batch was accepted")
				}
				if count := eventCount(t, pool); count != tt.wantCount {
					t.Errorf("Invalid batch persisted %d events", count)
				}
			})
		}
	})
	t.Run("inbox minimum utcyear", func(t *testing.T) {
		tt := struct {
			periodStart    time.Time
			periodDuration time.Duration
			wantStart      time.Time
			wantEnd        time.Time
			wantReceipt    time.Time
		}{
			periodStart:    time.Date(999, 12, 31, 23, 0, 0, 0, time.FixedZone("UTC-01", -60*60)),
			periodDuration: time.Microsecond,
			wantStart:      time.Date(1000, 1, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:        time.Date(1000, 1, 1, 0, 0, 0, 1_000, time.UTC),
			wantReceipt:    time.Date(1000, 1, 1, 0, 0, 0, 1_000, time.UTC),
		}

		pool := testDatabase(t, nil)
		event := fixtureEvents()[0]
		event.PeriodStart = tt.periodStart
		event.PeriodEnd = event.PeriodStart.Add(tt.periodDuration)
		receipt := event.PeriodEnd.UTC()
		if err := inbox.NewPostgres(pool, 5*time.Second).InsertBatch(context.Background(), []usage.Event{event}, receipt); err != nil {
			t.Fatalf("Persist minimum-year event: %v", err)
		}
		var start, end, received time.Time
		if err := pool.QueryRow(context.Background(), "SELECT period_start, period_end, received_at FROM usage_inbox").Scan(&start, &end, &received); err != nil {
			t.Fatalf("Read minimum-year event: %v", err)
		}
		if !start.Equal(tt.wantStart) || !end.Equal(tt.wantEnd) || !received.Equal(tt.wantReceipt) {
			t.Errorf("Stored minimum-year timestamps = %s, %s, %s; want %s, %s, %s", start, end, received, event.PeriodStart.UTC(), event.PeriodEnd.UTC(), receipt)
		}
	})
	t.Run("inbox cancelled context", func(t *testing.T) {
		tt := struct {
			events                     []usage.Event
			wantError                  error
			wantCountAfterCancellation int
		}{
			events:                     fixtureEvents(),
			wantError:                  context.Canceled,
			wantCountAfterCancellation: 0,
		}

		pool := testDatabase(t, nil)
		store := inbox.NewPostgres(pool, 5*time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := store.InsertBatch(ctx, tt.events, time.Now().UTC())
		if !errors.Is(err, tt.wantError) {
			t.Fatalf("Canceled insert error = %v, want context.Canceled", err)
		}
		if count := eventCount(t, pool); count != tt.wantCountAfterCancellation {
			t.Errorf("Canceled batch persisted %d events", count)
		}
		if err := store.InsertBatch(context.Background(), tt.events, time.Now().UTC()); err != nil {
			t.Fatalf("Pool unusable after canceled request: %v", err)
		}
	})
	t.Run("inbox synchronous commit", func(t *testing.T) {
		tt := struct {
			sessionSettings    map[string]string
			constraintSQL      string
			events             []usage.Event
			wantSessionSetting string
		}{
			sessionSettings:    map[string]string{"synchronous_commit": "off"},
			constraintSQL:      "ALTER TABLE usage_inbox ADD CHECK (current_setting('synchronous_commit') = 'on')",
			events:             fixtureEvents(),
			wantSessionSetting: "off",
		}

		pool := testDatabase(t, tt.sessionSettings)
		if _, err := pool.Exec(context.Background(), tt.constraintSQL); err != nil {
			t.Fatalf("Set durability verification constraint: %v", err)
		}
		if err := inbox.NewPostgres(pool, 5*time.Second).InsertBatch(context.Background(), tt.events, time.Now().UTC()); err != nil {
			t.Fatalf("Synchronous inbox commit: %v", err)
		}
		var setting string
		if err := pool.QueryRow(context.Background(), "SHOW synchronous_commit").Scan(&setting); err != nil {
			t.Fatalf("Read session setting: %v", err)
		}
		if setting != tt.wantSessionSetting {
			t.Errorf("Session setting leaked outside transaction: %q", setting)
		}
	})
	t.Run("inbox identical retry", func(t *testing.T) {
		tests := []struct {
			name              string
			state             string
			retryStartOffset  int
			retryEndOffset    int
			retryReceiptDelay time.Duration
			wantRowUnchanged  bool
			wantCount         int
		}{
			{
				name:              "pending",
				state:             "pending",
				retryStartOffset:  8 * 60 * 60,
				retryEndOffset:    -7 * 60 * 60,
				retryReceiptDelay: time.Hour,
				wantRowUnchanged:  true,
				wantCount:         1,
			},
			{
				name:              "processed",
				state:             "processed",
				retryStartOffset:  8 * 60 * 60,
				retryEndOffset:    -7 * 60 * 60,
				retryReceiptDelay: time.Hour,
				wantRowUnchanged:  true,
				wantCount:         1,
			},
			{
				name:              "error",
				state:             "error",
				retryStartOffset:  8 * 60 * 60,
				retryEndOffset:    -7 * 60 * 60,
				retryReceiptDelay: time.Hour,
				wantRowUnchanged:  true,
				wantCount:         1,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				pool := testDatabase(t, nil)
				store := inbox.NewPostgres(pool, 5*time.Second)
				event := fixtureEvents()[0]
				receipt := time.Date(2026, 11, 1, 0, 30, 0, 0, time.UTC)
				if err := store.InsertBatch(context.Background(), []usage.Event{event}, receipt); err != nil {
					t.Fatalf("Seed original event: %v", err)
				}
				switch tt.state {
				case "processed":
					if _, err := pool.Exec(context.Background(), "UPDATE usage_inbox SET processed_at = $1", receipt.Add(time.Minute)); err != nil {
						t.Fatalf("Set processed state: %v", err)
					}
				case "error":
					if _, err := pool.Exec(context.Background(), "UPDATE usage_inbox SET processing_error = 'accounting fixture failure'"); err != nil {
						t.Fatalf("Set processing error: %v", err)
					}
				}
				var before, after string
				if err := pool.QueryRow(context.Background(), "SELECT row_to_json(usage_inbox)::text FROM usage_inbox").Scan(&before); err != nil {
					t.Fatalf("Snapshot original row: %v", err)
				}
				event.PeriodStart = event.PeriodStart.In(time.FixedZone("retry start", tt.retryStartOffset))
				event.PeriodEnd = event.PeriodEnd.In(time.FixedZone("retry end", tt.retryEndOffset))
				if err := store.InsertBatch(context.Background(), []usage.Event{event}, receipt.Add(tt.retryReceiptDelay)); err != nil {
					t.Fatalf("Retry identical measurement: %v", err)
				}
				if err := pool.QueryRow(context.Background(), "SELECT row_to_json(usage_inbox)::text FROM usage_inbox").Scan(&after); err != nil {
					t.Fatalf("Read retried row: %v", err)
				}
				if (before == after) != tt.wantRowUnchanged || eventCount(t, pool) != tt.wantCount {
					t.Errorf("Identical retry changed the stored row: before %s, after %s", before, after)
				}
			})
		}
	})
	t.Run("inbox changed content", func(t *testing.T) {
		for _, tt := range []struct {
			name         string
			change       func(*usage.Event)
			wantCount    int
			wantConflict bool
		}{
			{
				name:         "schema version",
				change:       func(e *usage.Event) { e.SchemaVersion++ },
				wantCount:    1,
				wantConflict: true,
			},
			{
				name:         "customer",
				change:       func(e *usage.Event) { e.CustomerID += " " },
				wantCount:    1,
				wantConflict: true,
			},
			{
				name:         "sandbox",
				change:       func(e *usage.Event) { e.SandboxID += "-other" },
				wantCount:    1,
				wantConflict: true,
			},
			{
				name:         "metric",
				change:       func(e *usage.Event) { e.Metric = "ram_gb_seconds" },
				wantCount:    1,
				wantConflict: true,
			},
			{
				name:         "period start",
				change:       func(e *usage.Event) { e.PeriodStart = e.PeriodStart.Add(time.Microsecond) },
				wantCount:    1,
				wantConflict: true,
			},
			{
				name:         "period end",
				change:       func(e *usage.Event) { e.PeriodEnd = e.PeriodEnd.Add(time.Microsecond) },
				wantCount:    1,
				wantConflict: true,
			},
			{
				name:         "units",
				change:       func(e *usage.Event) { e.Units++ },
				wantCount:    1,
				wantConflict: true,
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				pool := testDatabase(t, nil)
				store := inbox.NewPostgres(pool, 5*time.Second)
				event := fixtureEvents()[0]
				if err := store.InsertBatch(context.Background(), []usage.Event{event}, time.Now().UTC()); err != nil {
					t.Fatalf("Seed original event: %v", err)
				}
				changed := event
				tt.change(&changed)
				err := store.InsertBatch(context.Background(), []usage.Event{changed}, time.Now().UTC())
				var conflict *inbox.ConflictError
				if errors.As(err, &conflict) != tt.wantConflict {
					t.Fatalf("InsertBatch error = %v; want conflict=%t", err, tt.wantConflict)
				}
				assertConflict(t, err, event)
				if count := eventCount(t, pool); count != tt.wantCount {
					t.Errorf("Conflict changed event count to %d", count)
				}
			})
		}
	})
	t.Run("inbox batch boundaries", func(t *testing.T) {
		tt := struct {
			events          []usage.Event
			otherSource     string
			newEventID      string
			wantCallerOrder []string
			wantCount       int
		}{
			events:          fixtureEvents(),
			otherSource:     "another-source",
			newEventID:      "third",
			wantCallerOrder: []string{"second", "first"},
			wantCount:       4,
		}

		pool := testDatabase(t, nil)
		store := inbox.NewPostgres(pool, 5*time.Second)
		events := tt.events
		request := []usage.Event{events[1], events[0], events[0]}
		if err := store.InsertBatch(context.Background(), request, time.Now().UTC()); err != nil {
			t.Fatalf("Insert repeated events: %v", err)
		}
		if request[0].EventID != tt.wantCallerOrder[0] || request[1].EventID != tt.wantCallerOrder[1] {
			t.Error("Repository reordered the caller's batch")
		}
		otherSource := events[0]
		otherSource.Source = tt.otherSource
		otherSource.Units++
		newEvent := events[0]
		newEvent.EventID = tt.newEventID
		if err := store.InsertBatch(context.Background(), []usage.Event{newEvent, events[1], otherSource, events[0]}, time.Now().UTC()); err != nil {
			t.Fatalf("Insert regrouped batch: %v", err)
		}
		if count := eventCount(t, pool); count != tt.wantCount {
			t.Errorf("Stored event count = %d, want 4 independent keys", count)
		}
	})
	t.Run("inbox conflicting duplicates", func(t *testing.T) {
		tt := struct {
			events       []usage.Event
			changedUnits int64
			wantCount    int
		}{
			events:       fixtureEvents(),
			changedUnits: 100_000_001,
			wantCount:    0,
		}

		pool := testDatabase(t, nil)
		events := tt.events
		changed := events[0]
		changed.Units = tt.changedUnits
		err := inbox.NewPostgres(pool, 5*time.Second).InsertBatch(context.Background(), []usage.Event{events[0], events[1], changed}, time.Now().UTC())
		assertConflict(t, err, changed)
		if count := eventCount(t, pool); count != tt.wantCount {
			t.Errorf("Conflicting batch persisted %d events", count)
		}
	})
	t.Run("inbox concurrent identical batches", func(t *testing.T) {
		tt := struct {
			events    []usage.Event
			writers   int
			wantCount int
		}{
			events:    fixtureEvents(),
			writers:   12,
			wantCount: 14,
		}

		pool := testDatabase(t, nil)
		store := inbox.NewPostgres(pool, 5*time.Second)
		events := tt.events
		writers := tt.writers
		start := make(chan struct{})
		results := make(chan error, writers)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for writer := range writers {
			go func() {
				extra := events[0]
				extra.EventID = fmt.Sprintf("extra-%02d", writer)
				batch := []usage.Event{events[0], events[1], extra}
				if writer%2 == 0 {
					batch = []usage.Event{extra, events[1], events[0]}
				}
				<-start
				results <- store.InsertBatch(ctx, batch, time.Now().UTC())
			}()
		}
		close(start)
		for range writers {
			if err := <-results; err != nil {
				t.Errorf("Concurrent identical batch: %v", err)
			}
		}
		if count := eventCount(t, pool); count != tt.wantCount {
			t.Errorf("Concurrent event count = %d, want %d", count, tt.wantCount)
		}
	})
	t.Run("inbox concurrent conflicting batches", func(t *testing.T) {
		tt := struct {
			original      usage.Event
			writers       int
			wantSuccesses int
			wantConflicts int
			wantCount     int
		}{
			original:      fixtureEvents()[0],
			writers:       2,
			wantSuccesses: 1,
			wantConflicts: 1,
			wantCount:     2,
		}

		pool := testDatabase(t, nil)
		store := inbox.NewPostgres(pool, 5*time.Second)
		original := tt.original
		start := make(chan struct{})
		results := make(chan error, 2)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for writer := range tt.writers {
			go func() {
				conflicting := original
				conflicting.Units += int64(writer)
				private := original
				private.EventID = fmt.Sprintf("000-private-%d", writer)
				<-start
				results <- store.InsertBatch(ctx, []usage.Event{private, conflicting}, time.Now().UTC())
			}()
		}
		close(start)
		successes, conflicts := 0, 0
		for range tt.writers {
			if err := <-results; err == nil {
				successes++
			} else {
				assertConflict(t, err, original)
				conflicts++
			}
		}
		if successes != tt.wantSuccesses || conflicts != tt.wantConflicts || eventCount(t, pool) != tt.wantCount {
			t.Errorf("Race result: %d successful, %d conflicts; want one complete winner", successes, conflicts)
		}
		var privateID string
		var units int64
		if err := pool.QueryRow(context.Background(), "SELECT event_id FROM usage_inbox WHERE event_id LIKE '000-private-%'").Scan(&privateID); err != nil {
			t.Fatalf("Read winning private event: %v", err)
		}
		if err := pool.QueryRow(context.Background(), "SELECT units FROM usage_inbox WHERE event_id = 'first'").Scan(&units); err != nil {
			t.Fatalf("Read winning measurement: %v", err)
		}
		if (privateID == "000-private-0" && units != original.Units) ||
			(privateID == "000-private-1" && units != original.Units+1) {
			t.Error("Stored measurement and private event came from different batches")
		}
	})
	t.Run("inbox waiting writer", func(t *testing.T) {
		tests := []struct {
			name          string
			holderOutcome string
			wantError     error
			wantCount     int
			wantReceipt   time.Time
		}{
			{
				name:          "commit",
				holderOutcome: "commit",
				wantError:     nil,
				wantCount:     2,
				wantReceipt:   time.Date(2026, 11, 1, 0, 30, 0, 0, time.UTC),
			},
			{
				name:          "rollback",
				holderOutcome: "rollback",
				wantError:     nil,
				wantCount:     2,
				wantReceipt:   time.Date(2026, 11, 1, 1, 30, 0, 0, time.UTC),
			},
			{
				name:          "cancel waiting request",
				holderOutcome: "cancel waiting request",
				wantError:     context.Canceled,
				wantCount:     0,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				pool := testDatabase(t, nil)
				application := pool.Config().ConnConfig.RuntimeParams["application_name"]
				holder, err := pool.Begin(context.Background())
				if err != nil {
					t.Fatalf("Begin competing transaction: %v", err)
				}
				defer holder.Rollback(context.Background())
				event := fixtureEvents()[0]
				originalReceipt := time.Date(2026, 11, 1, 0, 30, 0, 0, time.UTC)
				if _, err := holder.Exec(context.Background(), `
					INSERT INTO usage_inbox
						(source, event_id, schema_version, customer_id, sandbox_id, metric,
						 period_start, period_end, units, received_at, processed_at, processing_error)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULL,NULL)`,
					event.Source, event.EventID, event.SchemaVersion, event.CustomerID, event.SandboxID,
					event.Metric, event.PeriodStart, event.PeriodEnd, event.Units, originalReceipt,
				); err != nil {
					t.Fatalf("Hold competing insert: %v", err)
				}
				extra := event
				extra.EventID = "000-new-before-wait"
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				result := make(chan error, 1)
				store := inbox.NewPostgres(pool, 5*time.Second)
				go func() {
					result <- store.InsertBatch(ctx, []usage.Event{extra, event}, originalReceipt.Add(time.Hour))
				}()
				waitForRowLock(t, pool, application)
				switch tt.holderOutcome {
				case "commit":
					err = holder.Commit(context.Background())
				case "rollback":
					err = holder.Rollback(context.Background())
				case "cancel waiting request":
					cancel()
				}
				if err != nil {
					t.Fatalf("Release competing insert: %v", err)
				}
				err = <-result
				if tt.holderOutcome == "cancel waiting request" {
					if !errors.Is(err, tt.wantError) {
						t.Fatalf("Waiting insert error = %v, want context.Canceled", err)
					}
					if err := holder.Rollback(context.Background()); err != nil {
						t.Fatalf("Rollback competing insert: %v", err)
					}
					if count := eventCount(t, pool); count != tt.wantCount {
						t.Errorf("Canceled waiting batch persisted %d events", count)
					}
					if err := store.InsertBatch(context.Background(), []usage.Event{event}, originalReceipt); err != nil {
						t.Fatalf("Pool unusable after canceled transaction: %v", err)
					}
				} else {
					if err != nil || eventCount(t, pool) != tt.wantCount {
						t.Fatalf("Waiting batch: error %v; want two committed events", err)
					}
					var received time.Time
					if err := pool.QueryRow(context.Background(), "SELECT received_at FROM usage_inbox WHERE event_id = $1", event.EventID).Scan(&received); err != nil {
						t.Fatalf("Read winning receipt: %v", err)
					}
					expected := tt.wantReceipt
					if !received.Equal(expected) {
						t.Errorf("Winning receipt = %s, want %s", received, expected)
					}
				}
			})
		}
	})
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
		SandboxID: "sandbox-001", Metric: "cpu_seconds", Units: 100_000_000,
		PeriodStart: time.Date(2026, 10, 31, 23, 30, 0, 123_456_000, time.UTC),
		PeriodEnd:   time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
	}
	second := first
	second.EventID, second.CustomerID, second.Units = "second", "cyberdyne", 0
	second.PeriodStart = first.PeriodStart.In(time.FixedZone("UTC+08", 8*60*60))
	return []usage.Event{first, second}
}

// eventCount checks committed visibility through a separate pooled query, so
// rollback tests cannot accidentally inspect uncommitted transaction contents.
func eventCount(t testing.TB, pool *pgxpool.Pool) int {
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

// waitForRowLock observes actual PostgreSQL lock contention before releasing a
// competing writer. Its bounded polling makes snapshot and cancellation tests
// independent of goroutine scheduling or arbitrary fixed sleeps.
func waitForRowLock(t *testing.T, pool *pgxpool.Pool, application string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM pg_stat_activity
				WHERE application_name = $1 AND wait_event_type = 'Lock')`, application).Scan(&waiting)
		if err != nil {
			t.Fatalf("Observe waiting transaction: %v", err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("Writer never waited for the competing transaction")
		case <-ticker.C:
		}
	}
}
