//go:build integration

package inbox_test

import (
	"context"
	"errors"
	"testing"

	"e2b/billing-api/internal/billing"
)

// TestMonthlyLimitStatus exposes each exact gross input and literal outcome;
// the status reflects current configuration in the requested UTC usage month.
func TestMonthlyLimitStatus(t *testing.T) {
	cases := []struct {
		name        string
		limit       *int64
		gross       string
		month       string
		wantReached bool
		wantGross   string
	}{
		{name: "first assignment hour below limit", limit: limitValue(1500), gross: "617283945", month: "2026-10", wantGross: "617283945"},
		{name: "second hour crosses limit before credit", limit: limitValue(1500), gross: "1817283945", month: "2026-10", wantReached: true, wantGross: "1817283945"},
		{name: "raised limit clears status", limit: limitValue(2000), gross: "1817283945", month: "2026-10", wantGross: "1817283945"},
		{name: "new month resets gross", limit: limitValue(1500), gross: "1817283945", month: "2026-11", wantGross: "0"},
		{name: "zero limit reached with no spend", limit: limitValue(0), gross: "0", month: "2026-10", wantReached: true, wantGross: "0"},
		{name: "null means unlimited", gross: "1817283945", month: "2026-10", wantGross: "1817283945"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			store := billing.NewStore(pool)
			if _, err := pool.Exec(ctx, "INSERT INTO monthly_usage VALUES ('cyberdyne','2026-10-01',$1::numeric)", tc.gross); err != nil {
				t.Fatal(err)
			}
			if err := store.SetSpendLimit(ctx, "cyberdyne", billing.SpendLimitChange{IdempotencyKey: "limit", LimitCents: tc.limit}); err != nil {
				t.Fatal(err)
			}
			actual, err := store.MonthlyLimitStatus(ctx, "cyberdyne", tc.month)
			if err != nil {
				t.Fatal(err)
			}
			if actual.LimitReached != tc.wantReached || actual.GrossChargeTicks != tc.wantGross {
				t.Fatalf("MonthlyLimitStatus(month=%s limit=%v gross=%s): %+v want reached=%t gross=%s", tc.month, tc.limit, tc.gross, actual, tc.wantReached, tc.wantGross)
			}
		})
	}
}

// TestSetSpendLimit distinguishes retries from new changes, including returning
// to a previous amount, and checks conflicts without unintended state changes.
func TestSetSpendLimit(t *testing.T) {
	t.Run("old replay preserves new limit", func(t *testing.T) {
		scenario := struct {
			changes     []billing.SpendLimitChange
			wantLimit   int64
			wantVersion int64
		}{changes: []billing.SpendLimitChange{{IdempotencyKey: "first", LimitCents: limitValue(1500)}, {IdempotencyKey: "raised", LimitCents: limitValue(2000)}, {IdempotencyKey: "first", LimitCents: limitValue(1500)}}, wantLimit: 2000, wantVersion: 2}
		store := billing.NewStore(billingDatabase(t))
		ctx := context.Background()
		for _, change := range scenario.changes {
			if err := store.SetSpendLimit(ctx, "cyberdyne", change); err != nil {
				t.Fatal(err)
			}
		}
		actual, err := store.MonthlyLimitStatus(ctx, "cyberdyne", "2026-10")
		if err != nil {
			t.Fatal(err)
		}
		if actual.LimitCents == nil || *actual.LimitCents != scenario.wantLimit || actual.StateVersion != scenario.wantVersion {
			t.Fatalf("replayed limit=%+v want limit=%d version=%d", actual, scenario.wantLimit, scenario.wantVersion)
		}
	})

	cases := []struct {
		name           string
		customer       string
		month          string
		changes        []billing.SpendLimitChange
		wantError      error
		wantLimit      int64
		wantVersion    int64
		wantOperations int
	}{
		{
			name: "new operation can restore a previous limit", customer: "cyberdyne", month: "2026-10",
			changes: []billing.SpendLimitChange{
				{IdempotencyKey: "first", LimitCents: limitValue(1500)},
				{IdempotencyKey: "raised", LimitCents: limitValue(2000)},
				{IdempotencyKey: "restored", LimitCents: limitValue(1500)},
			},
			wantError: nil, wantLimit: 1500, wantVersion: 3, wantOperations: 3,
		},
		{
			name: "changed amount under the same operation identity conflicts", customer: "cyberdyne", month: "2026-10",
			changes: []billing.SpendLimitChange{
				{IdempotencyKey: "first", LimitCents: limitValue(1500)},
				{IdempotencyKey: "first", LimitCents: limitValue(2000)},
			},
			wantError: billing.ErrConflict, wantLimit: 1500, wantVersion: 1, wantOperations: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			store := billing.NewStore(pool)
			ctx := context.Background()

			var actualError error
			for _, change := range tc.changes {
				actualError = store.SetSpendLimit(ctx, tc.customer, change)
				if actualError != nil {
					break
				}
			}

			if !errors.Is(actualError, tc.wantError) {
				t.Fatalf("SetSpendLimit(customer=%s changes=%+v) error=%v; want %v", tc.customer, tc.changes, actualError, tc.wantError)
			}

			actual, err := store.MonthlyLimitStatus(ctx, tc.customer, tc.month)
			if err != nil {
				t.Fatalf("MonthlyLimitStatus(customer=%s month=%s) error=%v; want nil", tc.customer, tc.month, err)
			}

			if actual.LimitCents == nil || *actual.LimitCents != tc.wantLimit || actual.StateVersion != tc.wantVersion {
				t.Errorf("SetSpendLimit(customer=%s changes=%+v) state=%+v; want limit=%d version=%d", tc.customer, tc.changes, actual, tc.wantLimit, tc.wantVersion)
			}

			var operations int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM spend_limit_operations WHERE customer_id=$1", tc.customer).Scan(&operations); err != nil {
				t.Fatalf("Count spend-limit operations(customer=%s) error=%v; want nil", tc.customer, err)
			}

			if operations != tc.wantOperations {
				t.Errorf("SetSpendLimit(customer=%s changes=%+v) operations=%d; want %d", tc.customer, tc.changes, operations, tc.wantOperations)
			}
		})
	}
}

// TestSpendLimitAPI verifies explicit nullable limits and canonical month paths.
func TestSpendLimitAPI(t *testing.T) {
	t.Run("JSON idempotency keys", func(t *testing.T) {
		testCommandBodyKeys(t, commandKeyEndpoint{
			path:        "/customers/acme/spend-limit",
			body:        `{"limit_cents":1500}`,
			legacyBody:  `{"operation_id":"legacy","limit_cents":1500}`,
			wantSuccess: commandKeyState{limitOperations: 1, creditTicks: "0", version: 1, limit: limitValue(1500)},
		})
	})
	cases := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
	}{
		{name: "set finite limit", method: "POST", path: "/customers/cyberdyne/spend-limit", body: `{"idempotency_key":"limit","limit_cents":1500}`, wantStatus: 200},
		{name: "remove limit explicitly", method: "POST", path: "/customers/cyberdyne/spend-limit", body: `{"idempotency_key":"unlimited","limit_cents":null}`, wantStatus: 200},
		{name: "omitted limit is invalid", method: "POST", path: "/customers/cyberdyne/spend-limit", body: `{"idempotency_key":"limit"}`, wantStatus: 422},
		{name: "read monthly status", method: "GET", path: "/customers/cyberdyne/months/2026-10/limit-status", wantStatus: 200},
		{name: "noncanonical month rejected", method: "GET", path: "/customers/cyberdyne/months/2026-1/limit-status", wantStatus: 422},
		{name: "current UTC status", method: "GET", path: "/customers/cyberdyne/limit-status", wantStatus: 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := financialRequest(t, financialHandler(t), tc.method, tc.path, tc.body)
			if response.Code != tc.wantStatus {
				t.Fatalf("%s %s(%s): status=%d body=%s want %d", tc.method, tc.path, tc.body, response.Code, response.Body, tc.wantStatus)
			}
		})
	}
}

// limitValue supplies an address for the literal cents in a scenario; it does
// not transform money, select limits, or calculate expected outcomes.
func limitValue(cents int64) *int64 { return &cents }
