//go:build integration

package inbox_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/usage"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestInboxIdenticalRetry preserves the entire original row for pending,
// completed, and failed accounting states, even with a later receipt and a
// different textual time offset representing the same consumption instant.
func TestInboxIdenticalRetry(t *testing.T) {
	for _, state := range []string{"pending", "processed", "error"} {
		t.Run(state, func(t *testing.T) {
			pool := testDatabase(t, nil)
			store := inbox.NewPostgres(pool, 5*time.Second)
			event := fixtureEvents()[0]
			receipt := time.Date(2026, 11, 1, 0, 30, 0, 0, time.UTC)
			if err := store.InsertBatch(context.Background(), []usage.Event{event}, receipt); err != nil {
				t.Fatalf("Seed original event: %v", err)
			}
			switch state {
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
			event.PeriodStart = event.PeriodStart.In(time.FixedZone("UTC+08", 8*60*60))
			event.PeriodEnd = event.PeriodEnd.In(time.FixedZone("UTC-07", -7*60*60))
			if err := store.InsertBatch(context.Background(), []usage.Event{event}, receipt.Add(time.Hour)); err != nil {
				t.Fatalf("Retry identical measurement: %v", err)
			}
			if err := pool.QueryRow(context.Background(), "SELECT row_to_json(usage_inbox)::text FROM usage_inbox").Scan(&after); err != nil {
				t.Fatalf("Read retried row: %v", err)
			}
			if before != after || eventCount(t, pool) != 1 {
				t.Errorf("Identical retry changed the stored row: before %s, after %s", before, after)
			}
		})
	}
}

// TestInboxChangedContent checks every non-key measurement field independently.
// Reusing an existing key with any changed value must report a content conflict,
// including microsecond interval changes and identifiers that differ by spaces.
func TestInboxChangedContent(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*usage.Event)
	}{
		{"schema version", func(e *usage.Event) { e.SchemaVersion++ }},
		{"customer", func(e *usage.Event) { e.CustomerID += " " }},
		{"sandbox", func(e *usage.Event) { e.SandboxID += "-other" }},
		{"metric", func(e *usage.Event) { e.Metric = "ram_gb_seconds" }},
		{"period start", func(e *usage.Event) { e.PeriodStart = e.PeriodStart.Add(time.Microsecond) }},
		{"period end", func(e *usage.Event) { e.PeriodEnd = e.PeriodEnd.Add(time.Microsecond) }},
		{"units", func(e *usage.Event) { e.Units++ }},
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
			assertConflict(t, store.InsertBatch(context.Background(), []usage.Event{changed}, time.Now().UTC()), event)
			if count := eventCount(t, pool); count != 1 {
				t.Errorf("Conflict changed event count to %d", count)
			}
		})
	}
}

// TestInboxBatchBoundaries allows duplicate content within a batch and across
// regrouped retries. A source remains part of identity, caller ordering is
// preserved, and new events can commit alongside identical older measurements.
func TestInboxBatchBoundaries(t *testing.T) {
	pool := testDatabase(t, nil)
	store := inbox.NewPostgres(pool, 5*time.Second)
	events := fixtureEvents()
	request := []usage.Event{events[1], events[0], events[0]}
	if err := store.InsertBatch(context.Background(), request, time.Now().UTC()); err != nil {
		t.Fatalf("Insert repeated events: %v", err)
	}
	if request[0].EventID != events[1].EventID || request[1].EventID != events[0].EventID {
		t.Error("Repository reordered the caller's batch")
	}
	otherSource := events[0]
	otherSource.Source = "another-source"
	otherSource.Units++
	newEvent := events[0]
	newEvent.EventID = "third"
	if err := store.InsertBatch(context.Background(), []usage.Event{newEvent, events[1], otherSource, events[0]}, time.Now().UTC()); err != nil {
		t.Fatalf("Insert regrouped batch: %v", err)
	}
	if count := eventCount(t, pool); count != 4 {
		t.Errorf("Stored event count = %d, want 4 independent keys", count)
	}
}

// TestInboxConflictingDuplicates rejects different content for one key inside
// the same incoming batch, regardless of position. No new measurement can
// survive this conflict, including an otherwise valid independently keyed row.
func TestInboxConflictingDuplicates(t *testing.T) {
	pool := testDatabase(t, nil)
	events := fixtureEvents()
	changed := events[0]
	changed.Units++
	err := inbox.NewPostgres(pool, 5*time.Second).InsertBatch(context.Background(), []usage.Event{events[0], events[1], changed}, time.Now().UTC())
	assertConflict(t, err, changed)
	if count := eventCount(t, pool); count != 0 {
		t.Errorf("Conflicting batch persisted %d events", count)
	}
}

// TestInboxConcurrentIdenticalBatches starts twelve writers together with shared
// events in opposing orders and a private extra event each. All must commit
// without duplicates or deadlocks, including writers that wait on an insert
// absent from their initial READ COMMITTED statement snapshot.
func TestInboxConcurrentIdenticalBatches(t *testing.T) {
	pool := testDatabase(t, nil)
	store := inbox.NewPostgres(pool, 5*time.Second)
	events := fixtureEvents()
	const writers = 12
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
	if count := eventCount(t, pool); count != writers+2 {
		t.Errorf("Concurrent event count = %d, want %d", count, writers+2)
	}
}

// TestInboxConcurrentConflictingBatches races two different measurements for
// one identity. Exactly one complete batch wins; the loser reports a content
// conflict and rolls back its earlier private insert.
func TestInboxConcurrentConflictingBatches(t *testing.T) {
	pool := testDatabase(t, nil)
	store := inbox.NewPostgres(pool, 5*time.Second)
	original := fixtureEvents()[0]
	start := make(chan struct{})
	results := make(chan error, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for writer := range 2 {
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
	for range 2 {
		if err := <-results; err == nil {
			successes++
		} else {
			assertConflict(t, err, original)
			conflicts++
		}
	}
	if successes != 1 || conflicts != 1 || eventCount(t, pool) != 2 {
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
}

// TestInboxWaitingWriter deterministically holds a conflicting insert open
// until the next batch waits on its row lock. A commit must become visible to
// the retry, a rollback must allow insertion, and cancellation must roll back
// the waiting batch's earlier insert without damaging the pool.
func TestInboxWaitingWriter(t *testing.T) {
	for _, outcome := range []string{"commit", "rollback", "cancel waiting request"} {
		t.Run(outcome, func(t *testing.T) {
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
			switch outcome {
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
			if outcome == "cancel waiting request" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Waiting insert error = %v, want context.Canceled", err)
				}
				if err := holder.Rollback(context.Background()); err != nil {
					t.Fatalf("Rollback competing insert: %v", err)
				}
				if count := eventCount(t, pool); count != 0 {
					t.Errorf("Canceled waiting batch persisted %d events", count)
				}
				if err := store.InsertBatch(context.Background(), []usage.Event{event}, originalReceipt); err != nil {
					t.Fatalf("Pool unusable after canceled transaction: %v", err)
				}
			} else {
				if err != nil || eventCount(t, pool) != 2 {
					t.Fatalf("Waiting batch: error %v; want two committed events", err)
				}
				var received time.Time
				if err := pool.QueryRow(context.Background(), "SELECT received_at FROM usage_inbox WHERE event_id = $1", event.EventID).Scan(&received); err != nil {
					t.Fatalf("Read winning receipt: %v", err)
				}
				expected := originalReceipt
				if outcome == "rollback" {
					expected = expected.Add(time.Hour)
				}
				if !received.Equal(expected) {
					t.Errorf("Winning receipt = %s, want %s", received, expected)
				}
			}
		})
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
