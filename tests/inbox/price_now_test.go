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

// TestCreatePriceNow resolves server instants in private schemas, checks UTC and
// microsecond boundaries, and preserves rated history and financial state.
func TestCreatePriceNow(t *testing.T) {
	cases := []struct {
		name              string
		serverTime        string
		customer          string
		price             int64
		existingUsage     bool
		wantError         bool
		wantSentinel      error
		wantField         string
		wantEffectiveFrom string
		wantVersions      int
		wantRatings       int
	}{
		{name: "default activates immediately", serverTime: "2026-11-01T00:00:00Z", price: 7, wantEffectiveFrom: "2026-11-01T00:00:00Z", wantVersions: 4},
		{name: "override activates immediately", serverTime: "2026-11-01T00:00:00Z", customer: "acme", price: 7, wantEffectiveFrom: "2026-11-01T00:00:00Z", wantVersions: 4},
		{name: "UTC conversion truncates nanoseconds", serverTime: "2026-11-01T08:00:00.123456789+08:00", price: 7, wantEffectiveFrom: "2026-11-01T00:00:00.123456Z", wantVersions: 4},
		{name: "lower supported year", serverTime: "1000-01-01T00:00:00Z", price: 0, wantEffectiveFrom: "1000-01-01T00:00:00Z", wantVersions: 4},
		{name: "before lower supported year", serverTime: "0999-12-31T23:59:59.999999Z", price: 7, wantError: true, wantField: "effective_from", wantVersions: 3},
		{name: "upper supported year truncates safely", serverTime: "9999-12-31T23:59:59.999999999Z", price: 7, wantEffectiveFrom: "9999-12-31T23:59:59.999999Z", wantVersions: 4},
		{name: "negative price cannot be inserted", serverTime: "2026-11-01T00:00:00Z", price: -1, wantError: true, wantField: "price_per_million_cents", wantVersions: 3},
		{name: "cannot replace a rated price", serverTime: "2026-10-10T12:00:00Z", price: 7, existingUsage: true, wantError: true, wantSentinel: billing.ErrConflict, wantVersions: 3, wantRatings: 1},
		{name: "cannot split a rated interval", serverTime: "2026-10-10T12:30:00Z", price: 7, existingUsage: true, wantError: true, wantSentinel: billing.ErrConflict, wantVersions: 3, wantRatings: 1},
		{name: "rated interval end is valid", serverTime: "2026-10-10T13:00:00Z", price: 7, existingUsage: true, wantEffectiveFrom: "2026-10-10T13:00:00Z", wantVersions: 4, wantRatings: 1},
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
			price := billing.PriceInput{CustomerID: tc.customer, Metric: "cpu_seconds", PricePerMillionCents: tc.price}

			got, gotErr := store.CreatePriceNow(ctx, "automatic", price)

			if (gotErr != nil) != tc.wantError {
				t.Fatalf("CreatePriceNow(%+v, now=%s) error=%v; wantError=%t", price, tc.serverTime, gotErr, tc.wantError)
			}
			if tc.wantSentinel != nil && !errors.Is(gotErr, tc.wantSentinel) {
				t.Fatalf("CreatePriceNow(%+v) error=%v; want %v", price, gotErr, tc.wantSentinel)
			}
			if tc.wantField != "" {
				var validation *billing.ValidationError
				if !errors.As(gotErr, &validation) || validation.Field != tc.wantField {
					t.Fatalf("CreatePriceNow(%+v) error=%v; want validation field=%s", price, gotErr, tc.wantField)
				}
			}

			if tc.wantEffectiveFrom != "" {
				if resolved := got.EffectiveFrom.Format(time.RFC3339Nano); resolved != tc.wantEffectiveFrom {
					t.Errorf("CreatePriceNow(%+v) resolved time=%s; want %s", price, resolved, tc.wantEffectiveFrom)
				}
				var stored time.Time
				if err := pool.QueryRow(ctx, "SELECT effective_from FROM price_versions WHERE price_version_id=$1", got.ID).Scan(&stored); err != nil {
					t.Fatal(err)
				}
				if resolved := stored.UTC().Format(time.RFC3339Nano); resolved != tc.wantEffectiveFrom {
					t.Errorf("CreatePriceNow(%+v) stored time=%s; want %s", price, resolved, tc.wantEffectiveFrom)
				}
			}

			var versions, ratings int
			if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM price_versions),(SELECT count(*) FROM usage_ratings)").Scan(&versions, &ratings); err != nil {
				t.Fatal(err)
			}
			if versions != tc.wantVersions || ratings != tc.wantRatings {
				t.Errorf("CreatePriceNow(%+v) versions=%d ratings=%d; want %d and %d", price, versions, ratings, tc.wantVersions, tc.wantRatings)
			}

			after, err := readPriceBoundaryState(ctx, pool, "cyberdyne")
			if err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Errorf("CreatePriceNow(%+v) financial state=%+v; want unchanged %+v", price, after, before)
			}
		})
	}
}
