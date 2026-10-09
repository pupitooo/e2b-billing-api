//go:build integration

package inbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2b/billing-api/internal/accounting"
	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/usage"
)

// TestCreatePrice verifies explicit price commands in isolated catalogs. Rated
// history stays intact while future changes and unchanged retries succeed.
func TestCreatePrice(t *testing.T) {
	cases := []struct {
		name          string
		customer      string
		effective     string
		price         int64
		existingUsage bool
		repeatPrice   int64
		wantError     error
		wantVersions  int
	}{
		{name: "future default and identical retry", effective: "2026-12-01T00:00:00Z", price: 7, repeatPrice: 7, wantVersions: 4},
		{name: "changed identity content conflicts", effective: "2026-12-01T00:00:00Z", price: 7, repeatPrice: 8, wantError: billing.ErrConflict, wantVersions: 4},
		{name: "retroactive default cannot change rated history", effective: "2026-10-09T00:00:00Z", price: 7, existingUsage: true, repeatPrice: 7, wantError: billing.ErrConflict, wantVersions: 3},
		{name: "retroactive override cannot change rated history", customer: "cyberdyne", effective: "2026-10-09T00:00:00Z", price: 3, existingUsage: true, repeatPrice: 3, wantError: billing.ErrConflict, wantVersions: 3},
		{name: "missing customer has no new version", customer: "missing", effective: "2026-12-01T00:00:00Z", price: 7, repeatPrice: 7, wantError: billing.ErrNotFound, wantVersions: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			store := billing.NewStore(pool)
			if tc.existingUsage {
				event := usage.Event{Source: "history", EventID: "one", SchemaVersion: 1, CustomerID: "cyberdyne", SandboxID: "sandbox", Metric: "cpu_seconds", PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z"), Units: 123_456_789}
				if err := inbox.NewPostgres(pool, time.Second).InsertBatch(ctx, []usage.Event{event}, event.PeriodEnd); err != nil {
					t.Fatal(err)
				}
				if _, err := store.ProcessBatch(ctx); err != nil {
					t.Fatal(err)
				}
			}
			price := accounting.PriceVersion{ID: "new-version", CustomerID: tc.customer, Metric: "cpu_seconds", EffectiveFrom: parseBillingTime(t, tc.effective), PricePerMillionCents: tc.price}
			firstErr := store.CreatePrice(ctx, price)
			if firstErr == nil {
				price.PricePerMillionCents = tc.repeatPrice
				firstErr = store.CreatePrice(ctx, price)
			}
			if !errors.Is(firstErr, tc.wantError) {
				t.Fatalf("CreatePrice(customer=%q effective=%s price=%d repeat=%d): error=%v want %v", tc.customer, tc.effective, tc.price, tc.repeatPrice, firstErr, tc.wantError)
			}
			var versions int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM price_versions").Scan(&versions); err != nil {
				t.Fatal(err)
			}
			if versions != tc.wantVersions {
				t.Fatalf("price versions=%d want %d", versions, tc.wantVersions)
			}
		})
	}
}
