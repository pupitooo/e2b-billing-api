//go:build integration

package inbox_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"e2b/billing-api/internal/accounting"
	"e2b/billing-api/internal/billing"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testPriceLockWait observes PostgreSQL contention before advancing the server
// clock. Validation uses the instant after locking, with no writes on rejection.
func testPriceLockWait(t *testing.T) {
	cases := []struct {
		name          string
		arrivalTime   string
		insertionTime string
		effectiveFrom string
		wantError     bool
		wantField     string
		wantVersions  int
	}{
		{name: "activation becomes past while waiting", arrivalTime: "2026-11-01T00:00:00Z", insertionTime: "2026-11-01T00:00:02Z", effectiveFrom: "2026-11-01T00:00:01Z", wantError: true, wantField: "effective_from", wantVersions: 3},
		{name: "exact instant after waiting remains valid", arrivalTime: "2026-11-01T00:00:00Z", insertionTime: "2026-11-01T00:00:02Z", effectiveFrom: "2026-11-01T00:00:02Z", wantVersions: 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			config := pool.Config()
			config.ConnConfig.RuntimeParams["application_name"] = "price-activation-wait"
			writer, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			lock, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = lock.Rollback(context.Background()) }()
			if _, err := lock.Exec(ctx, "SELECT pg_advisory_xact_lock(65102,2)"); err != nil {
				t.Fatal(err)
			}
			var clock atomic.Value
			clock.Store(parseBillingTime(t, tc.arrivalTime))
			store := billing.NewStoreWithClock(writer, func() time.Time { return clock.Load().(time.Time) })
			price := accounting.PriceVersion{ID: "activation-wait", Metric: "cpu_seconds", PricePerMillionCents: 7, EffectiveFrom: parseBillingTime(t, tc.effectiveFrom)}
			result := make(chan error, 1)

			go func() { result <- store.CreatePrice(ctx, price) }()
			waitForRowLock(t, pool, "price-activation-wait")
			clock.Store(parseBillingTime(t, tc.insertionTime))
			if err := lock.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			gotErr := <-result

			if (gotErr != nil) != tc.wantError {
				t.Fatalf("CreatePrice(%+v) after lock wait error=%v; wantError=%t", price, gotErr, tc.wantError)
			}
			if tc.wantField != "" {
				var validation *billing.ValidationError
				if !errors.As(gotErr, &validation) || validation.Field != tc.wantField {
					t.Fatalf("CreatePrice(%+v) error=%v; want field=%s", price, gotErr, tc.wantField)
				}
			}
			var versions, ratings, entries int
			if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM price_versions),(SELECT count(*) FROM usage_ratings),(SELECT count(*) FROM credit_entries)").Scan(&versions, &ratings, &entries); err != nil {
				t.Fatal(err)
			}
			if versions != tc.wantVersions {
				t.Errorf("CreatePrice(%+v) versions=%d; want %d", price, versions, tc.wantVersions)
			}
			if ratings != 0 || entries != 0 {
				t.Errorf("CreatePrice(%+v) ratings=%d credit entries=%d; want 0 and 0", price, ratings, entries)
			}
		})
	}
}
