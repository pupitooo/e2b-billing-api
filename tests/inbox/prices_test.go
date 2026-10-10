//go:build integration

package inbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/usage"
)

// TestCreatePrice checks insertion-time boundaries, unchanged and conflicting
// retries, and rejection without catalog or financial effects in private schemas.
func TestCreatePrice(t *testing.T) {
	cases := []struct {
		name          string
		customer      string
		serverTime    string
		effective     string
		price         int64
		existingUsage bool
		repeatTime    string
		repeatPrice   int64
		wantError     bool
		wantSentinel  error
		wantField     string
		wantVersions  int
		wantRatings   int
	}{
		{name: "future default and identical retry", serverTime: "2026-11-01T00:00:00Z", effective: "2026-12-01T00:00:00Z", price: 7, repeatTime: "2027-01-01T00:00:00Z", repeatPrice: 7, wantVersions: 4},
		{name: "changed identity content conflicts after activation", serverTime: "2026-11-01T00:00:00Z", effective: "2026-12-01T00:00:00Z", price: 7, repeatTime: "2027-01-01T00:00:00Z", repeatPrice: 8, wantError: true, wantSentinel: billing.ErrConflict, wantVersions: 4},
		{name: "past default is rejected without rated history", serverTime: "2026-11-01T00:00:00Z", effective: "2026-10-09T00:00:00Z", price: 7, wantError: true, wantField: "effective_from", wantVersions: 3},
		{name: "past default leaves rated history intact", serverTime: "2026-11-01T00:00:00Z", effective: "2026-10-09T00:00:00Z", price: 7, existingUsage: true, wantError: true, wantField: "effective_from", wantVersions: 3, wantRatings: 1},
		{name: "past override leaves rated history intact", customer: "cyberdyne", serverTime: "2026-11-01T00:00:00Z", effective: "2026-10-09T00:00:00Z", price: 3, existingUsage: true, wantError: true, wantField: "effective_from", wantVersions: 3, wantRatings: 1},
		{name: "one microsecond before insertion is rejected", serverTime: "2026-11-01T00:00:00Z", effective: "2026-10-31T23:59:59.999999Z", price: 7, wantError: true, wantField: "effective_from", wantVersions: 3},
		{name: "exact insertion instant is accepted", serverTime: "2026-11-01T00:00:00Z", effective: "2026-11-01T00:00:00Z", price: 7, wantVersions: 4},
		{name: "one microsecond after insertion is accepted", serverTime: "2026-11-01T00:00:00Z", effective: "2026-11-01T00:00:00.000001Z", price: 7, wantVersions: 4},
		{name: "equivalent UTC offset at insertion is accepted", serverTime: "2026-11-01T00:00:00Z", effective: "2026-11-01T08:00:00+08:00", price: 7, wantVersions: 4},
		{name: "local future date cannot hide a past UTC instant", serverTime: "2026-11-01T00:00:00Z", effective: "2026-11-01T07:59:59.999999+08:00", price: 7, wantError: true, wantField: "effective_from", wantVersions: 3},
		{name: "future insertion cannot change an already rated default", serverTime: "2026-10-01T00:00:00Z", effective: "2026-10-09T00:00:00Z", price: 7, existingUsage: true, wantError: true, wantSentinel: billing.ErrConflict, wantVersions: 3, wantRatings: 1},
		{name: "future insertion cannot change an already rated override", customer: "cyberdyne", serverTime: "2026-10-01T00:00:00Z", effective: "2026-10-09T00:00:00Z", price: 3, existingUsage: true, wantError: true, wantSentinel: billing.ErrConflict, wantVersions: 3, wantRatings: 1},
		{name: "future insertion cannot split a rated interval", serverTime: "2026-10-01T00:00:00Z", effective: "2026-10-10T12:30:00Z", price: 7, existingUsage: true, wantError: true, wantSentinel: billing.ErrConflict, wantVersions: 3, wantRatings: 1},
		{name: "future insertion at rated interval end is harmless", serverTime: "2026-10-01T00:00:00Z", effective: "2026-10-10T13:00:00Z", price: 7, existingUsage: true, wantVersions: 4, wantRatings: 1},
		{name: "missing customer has no new version", customer: "missing", serverTime: "2026-11-01T00:00:00Z", effective: "2026-12-01T00:00:00Z", price: 7, wantError: true, wantSentinel: billing.ErrNotFound, wantVersions: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			serverTime := parseBillingTime(t, tc.serverTime)
			store := billing.NewStoreWithClock(pool, func() time.Time { return serverTime })
			if tc.existingUsage {
				event := usage.Event{Source: "history", EventID: "one", SchemaVersion: 1, CustomerID: "cyberdyne", SandboxID: "sandbox", Metric: "cpu_seconds", PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z"), Units: 123_456_789}
				if err := inbox.NewPostgres(pool, time.Second).InsertBatch(ctx, []usage.Event{event}, event.PeriodEnd); err != nil {
					t.Fatal(err)
				}
				if _, err := store.ProcessBatch(ctx); err != nil {
					t.Fatal(err)
				}
			}
			before, err := readPriceBoundaryState(ctx, pool, "cyberdyne")
			if err != nil {
				t.Fatal(err)
			}

			price := billing.PriceInput{CustomerID: tc.customer, Metric: "cpu_seconds", EffectiveFrom: parseBillingTime(t, tc.effective), PricePerMillionCents: tc.price}
			_, gotErr := store.CreatePrice(ctx, "new-version", price)
			if gotErr == nil && tc.repeatTime != "" {
				serverTime = parseBillingTime(t, tc.repeatTime)
				price.PricePerMillionCents = tc.repeatPrice
				_, gotErr = store.CreatePrice(ctx, "new-version", price)
			}

			if (gotErr != nil) != tc.wantError {
				t.Fatalf("CreatePrice(%+v, now=%s) error=%v; wantError=%t", price, serverTime, gotErr, tc.wantError)
			}
			if tc.wantSentinel != nil && !errors.Is(gotErr, tc.wantSentinel) {
				t.Fatalf("CreatePrice(%+v) error=%v; want %v", price, gotErr, tc.wantSentinel)
			}
			if tc.wantField != "" {
				var validation *billing.ValidationError
				if !errors.As(gotErr, &validation) || validation.Field != tc.wantField {
					t.Fatalf("CreatePrice(%+v) error=%v; want validation field=%s", price, gotErr, tc.wantField)
				}
			}

			var versions, ratings int
			if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM price_versions),(SELECT count(*) FROM usage_ratings)").Scan(&versions, &ratings); err != nil {
				t.Fatal(err)
			}
			if versions != tc.wantVersions {
				t.Errorf("CreatePrice(%+v) versions=%d; want %d", price, versions, tc.wantVersions)
			}
			if ratings != tc.wantRatings {
				t.Errorf("CreatePrice(%+v) ratings=%d; want %d", price, ratings, tc.wantRatings)
			}
			after, err := readPriceBoundaryState(ctx, pool, "cyberdyne")
			if err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Errorf("CreatePrice(%+v) financial state=%+v; want unchanged %+v", price, after, before)
			}
		})
	}

	t.Run("activation after a catalog lock wait", testPriceLockWait)
}
