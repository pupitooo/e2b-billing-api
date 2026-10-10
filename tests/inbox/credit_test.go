//go:build integration

package inbox_test

import (
	"context"
	"errors"
	"testing"

	"e2b/billing-api/internal/billing"
)

// TestGrantCredit keeps grant steps and final balance beside the scenario. Each
// private account verifies replay or conflict without changing another case.
func TestGrantCredit(t *testing.T) {
	t.Run("sequence IDs and replay", testCreditEntryIdentities)
	cases := []struct {
		name        string
		customer    string
		recordedAt  string
		grants      []billing.CreditGrant
		wantError   error
		wantBalance string
		wantEntries int
		wantVersion int64
	}{
		{name: "identical retry grants once", customer: "acme", recordedAt: "2026-10-01T00:00:00Z", grants: []billing.CreditGrant{{IdempotencyKey: "welcome", AmountCents: 2500}, {IdempotencyKey: "welcome", AmountCents: 2500}}, wantError: nil, wantBalance: "2500000000", wantEntries: 1, wantVersion: 1},
		{name: "changed amount conflicts", customer: "acme", recordedAt: "2026-10-01T00:00:00Z", grants: []billing.CreditGrant{{IdempotencyKey: "welcome", AmountCents: 2500}, {IdempotencyKey: "welcome", AmountCents: 2501}}, wantError: billing.ErrConflict, wantBalance: "2500000000", wantEntries: 1, wantVersion: 1},
		{name: "distinct grants add", customer: "acme", recordedAt: "2026-10-01T00:00:00Z", grants: []billing.CreditGrant{{IdempotencyKey: "welcome", AmountCents: 2500}, {IdempotencyKey: "promotion", AmountCents: 500}}, wantError: nil, wantBalance: "3000000000", wantEntries: 2, wantVersion: 2},
		{name: "distinct identities with identical amounts and timestamps grant twice", customer: "acme", recordedAt: "2026-10-01T00:00:00Z", grants: []billing.CreditGrant{{IdempotencyKey: "welcome", AmountCents: 2500}, {IdempotencyKey: "promotion", AmountCents: 2500}}, wantError: nil, wantBalance: "5000000000", wantEntries: 2, wantVersion: 2},
		{name: "unknown customer", customer: "missing", recordedAt: "2026-10-01T00:00:00Z", grants: []billing.CreditGrant{{IdempotencyKey: "welcome", AmountCents: 2500}}, wantError: billing.ErrNotFound, wantEntries: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			store := billing.NewStore(pool)
			ctx := context.Background()
			var actualError error
			for _, grant := range tc.grants {
				grant.RecordedAt = parseBillingTime(t, tc.recordedAt)
				actualError = store.GrantCredit(ctx, tc.customer, grant)
				if actualError != nil {
					break
				}
			}
			if !errors.Is(actualError, tc.wantError) {
				t.Fatalf("GrantCredit(customer=%s grants=%+v): error=%v want %v", tc.customer, tc.grants, actualError, tc.wantError)
			}
			var entries int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM credit_entries").Scan(&entries); err != nil {
				t.Fatal(err)
			}
			if entries != tc.wantEntries {
				t.Fatalf("grant entries=%d want %d", entries, tc.wantEntries)
			}
			if tc.wantBalance != "" {
				snapshot, err := store.Credit(ctx, tc.customer)
				if err != nil {
					t.Fatal(err)
				}
				if snapshot.BalanceTicks != tc.wantBalance || snapshot.StateVersion != tc.wantVersion {
					t.Fatalf("Credit(%s)=%+v want balance=%s version=%d", tc.customer, snapshot, tc.wantBalance, tc.wantVersion)
				}
			}
		})
	}
}

// TestCreditAPI verifies grant validation and the exact public balance query.
// The expected response is literal JSON, independent of accounting helpers.
func TestCreditAPI(t *testing.T) {
	t.Run("JSON idempotency keys", func(t *testing.T) {
		testCommandBodyKeys(t, commandKeyEndpoint{
			path:        "/customers/acme/credits",
			body:        `{"amount_cents":1,"recorded_at":"2026-10-01T00:00:00Z"}`,
			legacyBody:  `{"operation_id":"legacy","amount_cents":1,"recorded_at":"2026-10-01T00:00:00Z"}`,
			wantSuccess: commandKeyState{creditEntries: 1, creditTicks: "1000000", version: 1},
		})
	})
	cases := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{name: "empty account", method: "GET", path: "/customers/acme/credit", wantStatus: 200, wantBody: `{"customer_id":"acme","credit_balance_ticks":"0","state_version":0,"pending_events":0,"processing_errors":0}`},
		{name: "missing account", method: "GET", path: "/customers/missing/credit", wantStatus: 404},
		{name: "explicit grant", method: "POST", path: "/customers/acme/credits", body: `{"idempotency_key":"welcome","amount_cents":2500,"recorded_at":"2026-10-01T00:00:00Z"}`, wantStatus: 200, wantBody: `{"idempotency_key":"welcome","amount_cents":2500,"recorded_at":"2026-10-01T00:00:00Z"}`},
		{name: "zero grant rejected", method: "POST", path: "/customers/acme/credits", body: `{"idempotency_key":"welcome","amount_cents":0,"recorded_at":"2026-10-01T00:00:00Z"}`, wantStatus: 422},
		{name: "amount is required", method: "POST", path: "/customers/acme/credits", body: `{"idempotency_key":"welcome","recorded_at":"2026-10-01T00:00:00Z"}`, wantStatus: 422},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := financialRequest(t, financialHandler(t), tc.method, tc.path, tc.body)
			if response.Code != tc.wantStatus {
				t.Fatalf("%s %s(%s): status=%d body=%s want %d", tc.method, tc.path, tc.body, response.Code, response.Body, tc.wantStatus)
			}
			if tc.wantBody != "" && response.Body.String() != tc.wantBody+"\n" {
				t.Fatalf("%s %s: body=%s want %s", tc.method, tc.path, response.Body, tc.wantBody)
			}
		})
	}
}
