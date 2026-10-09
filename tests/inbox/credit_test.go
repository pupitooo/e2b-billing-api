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
	cases := []struct {
		name        string
		customer    string
		grants      []billing.CreditGrant
		wantError   error
		wantBalance string
		wantEntries int
		wantVersion int64
	}{
		{name: "identical retry grants once", customer: "acme", grants: []billing.CreditGrant{{OperationID: "welcome", AmountCents: 2500}, {OperationID: "welcome", AmountCents: 2500}}, wantBalance: "2500000000", wantEntries: 1, wantVersion: 1},
		{name: "changed amount conflicts", customer: "acme", grants: []billing.CreditGrant{{OperationID: "welcome", AmountCents: 2500}, {OperationID: "welcome", AmountCents: 2501}}, wantError: billing.ErrConflict, wantBalance: "2500000000", wantEntries: 1, wantVersion: 1},
		{name: "distinct grants add", customer: "acme", grants: []billing.CreditGrant{{OperationID: "welcome", AmountCents: 2500}, {OperationID: "promotion", AmountCents: 500}}, wantBalance: "3000000000", wantEntries: 2, wantVersion: 2},
		{name: "unknown customer", customer: "missing", grants: []billing.CreditGrant{{OperationID: "welcome", AmountCents: 2500}}, wantError: billing.ErrNotFound, wantEntries: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			store := billing.NewStore(pool)
			ctx := context.Background()
			var actualError error
			for _, grant := range tc.grants {
				grant.RecordedAt = parseBillingTime(t, "2026-10-01T00:00:00Z")
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
		{name: "explicit grant", method: "POST", path: "/customers/acme/credits", body: `{"operation_id":"welcome","amount_cents":2500,"recorded_at":"2026-10-01T00:00:00Z"}`, wantStatus: 200, wantBody: `{"operation_id":"welcome","amount_cents":2500,"recorded_at":"2026-10-01T00:00:00Z"}`},
		{name: "zero grant rejected", method: "POST", path: "/customers/acme/credits", body: `{"operation_id":"welcome","amount_cents":0,"recorded_at":"2026-10-01T00:00:00Z"}`, wantStatus: 422},
		{name: "amount is required", method: "POST", path: "/customers/acme/credits", body: `{"operation_id":"welcome","recorded_at":"2026-10-01T00:00:00Z"}`, wantStatus: 422},
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
