//go:build integration

package inbox_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/usage"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestProcessBatch rates receipts in a private seeded schema. Every scenario
// declares consumption, credit, closure state, and exact persisted outcomes.
func TestProcessBatch(t *testing.T) {
	cases := []struct {
		name             string
		customer         string
		units            []int64
		schemaVersion    int32
		start            string
		end              string
		receiptTime      string
		creditTicks      string
		closedMonth      string
		wantGross        string
		wantCredit       string
		wantRemaining    string
		wantCents        int64
		wantRatings      int
		wantError        bool
		wantBillingMonth string
	}{
		{name: "override survives default change", customer: "acme", units: []int64{200_000_000}, schemaVersion: 1, start: "2026-10-20T12:00:00Z", end: "2026-10-20T13:00:00Z", receiptTime: "2026-11-01T00:00:00Z", creditTicks: "2500000000", wantGross: "800000000", wantCredit: "800000000", wantRemaining: "1700000000", wantCents: 800, wantRatings: 1, wantBillingMonth: "2026-10-01"},
		{name: "split receipts round cumulative group", customer: "cyberdyne", units: []int64{60_000, 60_000}, schemaVersion: 1, start: "2026-10-10T12:00:00Z", end: "2026-10-10T13:00:00Z", receiptTime: "2026-10-11T00:00:00Z", creditTicks: "0", wantGross: "600000", wantCredit: "0", wantRemaining: "0", wantCents: 1, wantRatings: 2, wantBillingMonth: "2026-10-01"},
		{name: "fractional credit stays exact", customer: "acme", units: []int64{1_250}, schemaVersion: 1, start: "2026-10-10T12:00:00Z", end: "2026-10-10T13:00:00Z", receiptTime: "2026-10-11T00:00:00Z", creditTicks: "1000000", wantGross: "5000", wantCredit: "5000", wantRemaining: "995000", wantCents: 0, wantRatings: 1, wantBillingMonth: "2026-10-01"},
		{name: "late usage retains original price and spend month", customer: "acme", units: []int64{50_000_000}, schemaVersion: 1, start: "2026-10-30T12:00:00Z", end: "2026-10-30T13:00:00Z", receiptTime: "2026-11-01T00:00:00Z", creditTicks: "0", closedMonth: "2026-10-01", wantGross: "200000000", wantCredit: "0", wantRemaining: "0", wantCents: 200, wantRatings: 1, wantBillingMonth: "2026-11-01"},
		{name: "unsupported schema is visible without charges", customer: "acme", units: []int64{100}, schemaVersion: 2, start: "2026-10-10T12:00:00Z", end: "2026-10-10T13:00:00Z", receiptTime: "2026-10-11T00:00:00Z", creditTicks: "1000000", wantGross: "0", wantCredit: "0", wantRemaining: "1000000", wantRatings: 0, wantError: true},
		{name: "crossing price boundary is visible without charges", customer: "cyberdyne", units: []int64{100}, schemaVersion: 1, start: "2026-10-14T23:30:00Z", end: "2026-10-15T00:30:00Z", receiptTime: "2026-10-16T00:00:00Z", creditTicks: "0", wantGross: "0", wantCredit: "0", wantRemaining: "0", wantRatings: 0, wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			if _, err := pool.Exec(ctx, "UPDATE customer_billing_state SET credit_balance_ticks=$2::numeric WHERE customer_id=$1", tc.customer, tc.creditTicks); err != nil {
				t.Fatal(err)
			}
			if tc.closedMonth != "" {
				if _, err := pool.Exec(ctx, "INSERT INTO closed_billing_months VALUES ($1,$2,$3)", tc.customer, tc.closedMonth, parseBillingTime(t, tc.receiptTime)); err != nil {
					t.Fatal(err)
				}
			}
			events := make([]usage.Event, len(tc.units))
			for i, units := range tc.units {
				events[i] = usage.Event{Source: "accounting-test", EventID: tc.name + string(rune('a'+i)), SchemaVersion: tc.schemaVersion, CustomerID: tc.customer, SandboxID: "sandbox", Metric: "cpu_seconds", PeriodStart: parseBillingTime(t, tc.start), PeriodEnd: parseBillingTime(t, tc.end), Units: units}
			}
			if err := inbox.NewPostgres(pool, time.Second).InsertBatch(ctx, events, parseBillingTime(t, tc.receiptTime)); err != nil {
				t.Fatal(err)
			}
			processor := billing.NewStore(pool)
			for range len(events) + 1 {
				if _, err := processor.ProcessBatch(ctx); err != nil {
					t.Fatalf("ProcessBatch(%s): %v", tc.name, err)
				}
			}
			var gross, credit, remaining string
			var cents int64
			var ratings int
			var failed bool
			err := pool.QueryRow(ctx, `SELECT COALESCE(sum(exact_charge_ticks),0)::text,
            COALESCE(sum(allocated_credit_ticks),0)::text,COALESCE(sum(booked_charge_cents),0)::bigint FROM rated_usage_groups`).Scan(&gross, &credit, &cents)
			if err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, "SELECT credit_balance_ticks::text FROM customer_billing_state WHERE customer_id=$1", tc.customer).Scan(&remaining); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM usage_ratings").Scan(&ratings); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, "SELECT bool_or(processing_error IS NOT NULL) FROM usage_inbox").Scan(&failed); err != nil {
				t.Fatal(err)
			}
			if gross != tc.wantGross || credit != tc.wantCredit || remaining != tc.wantRemaining || cents != tc.wantCents || ratings != tc.wantRatings || failed != tc.wantError {
				t.Fatalf("ProcessBatch(customer=%s units=%v): gross=%s credit=%s remaining=%s cents=%d ratings=%d failed=%t; want %s %s %s %d %d %t", tc.customer, tc.units, gross, credit, remaining, cents, ratings, failed, tc.wantGross, tc.wantCredit, tc.wantRemaining, tc.wantCents, tc.wantRatings, tc.wantError)
			}
			if tc.wantBillingMonth != "" {
				var month string
				if err := pool.QueryRow(ctx, "SELECT billing_month::text FROM rated_usage_groups").Scan(&month); err != nil {
					t.Fatal(err)
				}
				if month != tc.wantBillingMonth {
					t.Fatalf("billing month=%s want %s", month, tc.wantBillingMonth)
				}
			}
		})
	}
	t.Run("late database failure rolls back all financial effects", func(t *testing.T) {
		scenario := struct {
			units         int64
			creditTicks   string
			wantError     bool
			wantRatings   int
			wantCredit    string
			wantCompleted bool
		}{
			units: 100_000_000, creditTicks: "2500000000", wantError: true, wantRatings: 0, wantCredit: "2500000000", wantCompleted: false,
		}
		pool := billingDatabase(t)
		ctx := context.Background()
		if _, err := pool.Exec(ctx, "UPDATE customer_billing_state SET credit_balance_ticks=$1::numeric WHERE customer_id='acme'", scenario.creditTicks); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "ALTER TABLE usage_inbox ADD CHECK (processed_at IS NULL)"); err != nil {
			t.Fatal(err)
		}
		event := usage.Event{Source: "rollback", EventID: "one", SchemaVersion: 1, CustomerID: "acme", SandboxID: "sandbox", Metric: "cpu_seconds", PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z"), Units: scenario.units}
		if err := inbox.NewPostgres(pool, time.Second).InsertBatch(ctx, []usage.Event{event}, event.PeriodEnd); err != nil {
			t.Fatal(err)
		}
		_, err := billing.NewStore(pool).ProcessBatch(ctx)
		if (err != nil) != scenario.wantError {
			t.Fatalf("ProcessBatch with failed completion: error=%v wantError=%t", err, scenario.wantError)
		}
		var ratings int
		var credit string
		var completed bool
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM usage_ratings").Scan(&ratings); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, "SELECT credit_balance_ticks::text FROM customer_billing_state WHERE customer_id='acme'").Scan(&credit); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, "SELECT processed_at IS NOT NULL FROM usage_inbox").Scan(&completed); err != nil {
			t.Fatal(err)
		}
		if ratings != scenario.wantRatings || credit != scenario.wantCredit || completed != scenario.wantCompleted {
			t.Fatalf("failed completion: ratings=%d credit=%s completed=%t; want %d %s %t", ratings, credit, completed, scenario.wantRatings, scenario.wantCredit, scenario.wantCompleted)
		}
	})
	t.Run("concurrent workers and replay charge once", func(t *testing.T) {
		scenario := struct {
			units       int64
			workers     int
			wantRatings int
			wantGross   string
		}{units: 100_000_000, workers: 8, wantRatings: 1, wantGross: "400000000"}
		pool := billingDatabase(t)
		ctx := context.Background()
		event := usage.Event{Source: "concurrent", EventID: "one", SchemaVersion: 1, CustomerID: "acme", SandboxID: "sandbox", Metric: "cpu_seconds", PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z"), Units: scenario.units}
		store := inbox.NewPostgres(pool, time.Second)
		if err := store.InsertBatch(ctx, []usage.Event{event}, event.PeriodEnd); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		results := make(chan error, scenario.workers)
		for range scenario.workers {
			wg.Add(1)
			go func() { defer wg.Done(); _, err := billing.NewStore(pool).ProcessBatch(ctx); results <- err }()
		}
		wg.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Fatalf("concurrent ProcessBatch: %v", err)
			}
		}
		if err := store.InsertBatch(ctx, []usage.Event{event}, event.PeriodEnd.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := billing.NewStore(pool).ProcessBatch(ctx); err != nil {
			t.Fatal(err)
		}
		var count int
		var gross string
		if err := pool.QueryRow(ctx, "SELECT count(*),sum(exact_charge_ticks)::text FROM rated_usage_groups").Scan(&count, &gross); err != nil {
			t.Fatal(err)
		}
		if count != scenario.wantRatings || gross != scenario.wantGross {
			t.Fatalf("concurrent replay: groups=%d gross=%s want %d %s", count, gross, scenario.wantRatings, scenario.wantGross)
		}
	})
}

// billingDatabase adds every financial migration to the private inbox schema;
// no scenario can observe or mutate a running worker's application accounts.
func billingDatabase(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool := testDatabase(t, nil)
	files, err := filepath.Glob("../../migrations/[0-9][0-9][0-9]_*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files[1:] {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(context.Background(), string(data)); err != nil {
			t.Fatalf("apply %s: %v", file, err)
		}
	}
	return pool
}

// parseBillingTime converts only explicit scenario timestamps; it supplies no
// consumption inputs or financial expectations on behalf of a test case.
func parseBillingTime(t testing.TB, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
