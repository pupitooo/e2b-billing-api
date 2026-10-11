//go:build integration

package inbox_test

import (
	"context"
	"e2b/billing-api/internal/postgres"
	"errors"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
)

// TestGrantCredit keeps grant steps and final balance beside the scenario. Each
// private account verifies replay or conflict without changing another case.
func TestGrantCredit(t *testing.T) {
	t.Run("sequence IDs and replay", testCreditEntryIdentities)
	t.Run("server timestamp and replay", testCreditGrantTimestampIdentity)
	t.Run("server clock boundaries", testCreditServerClock)
	cases := []struct {
		name        string
		customer    string
		serverTime  string
		grants      []billing.CreditGrant
		wantError   error
		wantBalance string
		wantEntries int
		wantVersion int64
	}{
		{name: "identical retry grants once", customer: "acme", serverTime: "2026-10-01T00:00:00Z", grants: []billing.CreditGrant{{IdempotencyKey: "welcome", AmountCents: 2500}, {IdempotencyKey: "welcome", AmountCents: 2500}}, wantError: nil, wantBalance: "2500000000", wantEntries: 1, wantVersion: 1},
		{name: "changed amount conflicts", customer: "acme", serverTime: "2026-10-01T00:00:00Z", grants: []billing.CreditGrant{{IdempotencyKey: "welcome", AmountCents: 2500}, {IdempotencyKey: "welcome", AmountCents: 2501}}, wantError: billing.ErrConflict, wantBalance: "2500000000", wantEntries: 1, wantVersion: 1},
		{name: "distinct grants add", customer: "acme", serverTime: "2026-10-01T00:00:00Z", grants: []billing.CreditGrant{{IdempotencyKey: "welcome", AmountCents: 2500}, {IdempotencyKey: "promotion", AmountCents: 500}}, wantError: nil, wantBalance: "3000000000", wantEntries: 2, wantVersion: 2},
		{name: "distinct identities with identical amounts and timestamps grant twice", customer: "acme", serverTime: "2026-10-01T00:00:00Z", grants: []billing.CreditGrant{{IdempotencyKey: "welcome", AmountCents: 2500}, {IdempotencyKey: "promotion", AmountCents: 2500}}, wantError: nil, wantBalance: "5000000000", wantEntries: 2, wantVersion: 2},
		{name: "unknown customer", customer: "missing", serverTime: "2026-10-01T00:00:00Z", grants: []billing.CreditGrant{{IdempotencyKey: "welcome", AmountCents: 2500}}, wantError: billing.ErrNotFound, wantEntries: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			serverTime := parseBillingTime(t, tc.serverTime)
			store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return serverTime }})
			ctx := context.Background()
			var actualError error
			for _, grant := range tc.grants {
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
				if snapshot.BalanceMicrocents != tc.wantBalance || snapshot.StateVersion != tc.wantVersion {
					t.Fatalf("Credit(%s)=%+v want balance=%s version=%d", tc.customer, snapshot, tc.wantBalance, tc.wantVersion)
				}
			}
		})
	}
}

// TestCreditAPI verifies grant validation and the exact public balance query.
// The expected response is literal JSON, independent of accounting helpers.
func TestCreditAPI(t *testing.T) {
	t.Run("rejection preserves complete financial state", testCreditRejectedState)
	t.Run("invalid backend clock is a server failure", testCreditAPIClockFailure)
	t.Run("JSON idempotency keys", func(t *testing.T) {
		testCommandBodyKeys(t, commandKeyEndpoint{
			path:        "/customers/acme/credits",
			body:        `{"amount_cents":1}`,
			legacyBody:  `{"operation_id":"legacy","amount_cents":1}`,
			wantSuccess: commandKeyState{creditEntries: 1, creditMicrocents: "1000000", version: 1},
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
		{name: "empty account", method: "GET", path: "/customers/acme/credit", wantStatus: 200, wantBody: `{"customer_id":"acme","credit_balance_microcents":"0","state_version":0,"pending_events":0,"processing_errors":0}`},
		{name: "missing account", method: "GET", path: "/customers/missing/credit", wantStatus: 404},
		{name: "explicit grant", method: "POST", path: "/customers/acme/credits", body: `{"idempotency_key":"welcome","amount_cents":2500}`, wantStatus: 200, wantBody: `{"idempotency_key":"welcome","amount_cents":2500}`},
		{name: "caller audit timestamp is an unknown field", method: "POST", path: "/customers/acme/credits", body: `{"idempotency_key":"welcome","amount_cents":2500,"recorded_at":"2026-10-01T00:00:00Z"}`, wantStatus: 400},
		{name: "zero grant rejected", method: "POST", path: "/customers/acme/credits", body: `{"idempotency_key":"welcome","amount_cents":0}`, wantStatus: 422},
		{name: "amount is required", method: "POST", path: "/customers/acme/credits", body: `{"idempotency_key":"welcome"}`, wantStatus: 422},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			before := addonFinancialSnapshot(t, pool)
			response := financialRequest(t, resourceHandler(t, pool), tc.method, tc.path, tc.body)
			if response.Code != tc.wantStatus {
				t.Fatalf("%s %s(%s): status=%d body=%s want %d", tc.method, tc.path, tc.body, response.Code, response.Body, tc.wantStatus)
			}
			if tc.wantStatus != 200 {
				if after := addonFinancialSnapshot(t, pool); after != before {
					t.Fatalf("Rejected command(%s) changed state: before=%s after=%s", tc.body, before, after)
				}
			}
			if tc.wantBody != "" && response.Body.String() != tc.wantBody+"\n" {
				t.Fatalf("%s %s: body=%s want %s", tc.method, tc.path, response.Body, tc.wantBody)
			}
		})
	}
}

// testCreditServerClock verifies supported backend audit instants and rejects an
// invalid server year without any ledger, balance, version, or command write.
func testCreditServerClock(t *testing.T) {
	cases := []struct {
		name             string
		serverTime       string
		wantRecordedAt   string
		wantError        bool
		wantErrorMessage string
		wantEntries      int
		wantBalance      string
		wantVersion      int64
		wantCommands     int
	}{
		{name: "first supported instant", serverTime: "1000-01-01T00:00:00Z", wantRecordedAt: "1000-01-01T00:00:00Z", wantEntries: 1, wantBalance: "1000000", wantVersion: 1, wantCommands: 1},
		{name: "last instant truncates safely", serverTime: "9999-12-31T23:59:59.999999999Z", wantRecordedAt: "9999-12-31T23:59:59.999999Z", wantEntries: 1, wantBalance: "1000000", wantVersion: 1, wantCommands: 1},
		{name: "offset enters supported range", serverTime: "0999-12-31T23:00:00-01:00", wantRecordedAt: "1000-01-01T00:00:00Z", wantEntries: 1, wantBalance: "1000000", wantVersion: 1, wantCommands: 1},
		{name: "before supported year", serverTime: "0999-12-31T23:59:59.999999Z", wantError: true, wantErrorMessage: "credit audit clock is outside the supported timestamp range", wantEntries: 0, wantBalance: "0", wantVersion: 0, wantCommands: 0},
		{name: "offset exceeds supported year", serverTime: "9999-12-31T23:59:59-01:00", wantError: true, wantErrorMessage: "credit audit clock is outside the supported timestamp range", wantEntries: 0, wantBalance: "0", wantVersion: 0, wantCommands: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			serverTime := parseBillingTime(t, tc.serverTime)
			store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return serverTime }})
			grant := billing.CreditGrant{IdempotencyKey: "server-clock", AmountCents: 1}

			err := store.GrantCredit(ctx, "acme", grant)

			if (err != nil) != tc.wantError {
				t.Fatalf("GrantCredit(%+v clock=%s) error=%v; wantError=%t", grant, tc.serverTime, err, tc.wantError)
			}
			if tc.wantErrorMessage != "" && (err == nil || err.Error() != tc.wantErrorMessage) {
				t.Fatalf("GrantCredit(clock=%s) error=%v; want message=%q", tc.serverTime, err, tc.wantErrorMessage)
			}

			var entries, commands int
			var balance string
			var version int64
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM credit_entries),
        (SELECT count(*) FROM api_idempotency_operations),credit_balance_microcents::text,state_version
        FROM customer_billing_state WHERE customer_id='acme'`).Scan(&entries, &commands, &balance, &version); err != nil {
				t.Fatal(err)
			}

			if entries != tc.wantEntries || commands != tc.wantCommands || balance != tc.wantBalance || version != tc.wantVersion {
				t.Errorf("GrantCredit(clock=%s) entries=%d commands=%d balance=%s version=%d; want %d,%d,%s,%d", tc.serverTime, entries, commands, balance, version, tc.wantEntries, tc.wantCommands, tc.wantBalance, tc.wantVersion)
			}
			if tc.wantRecordedAt == "" {
				return
			}

			var recordedAt time.Time
			if err := pool.QueryRow(ctx, "SELECT recorded_at FROM credit_entries").Scan(&recordedAt); err != nil {
				t.Fatal(err)
			}

			if !recordedAt.Equal(parseBillingTime(t, tc.wantRecordedAt)) {
				t.Errorf("GrantCredit(clock=%s) recorded_at=%s; want %s", tc.serverTime, recordedAt, tc.wantRecordedAt)
			}
		})
	}
}

// testCreditAPIClockFailure keeps invalid server audit time out of public field
// errors and verifies that no ledger, account, or command-history write commits.
func testCreditAPIClockFailure(t *testing.T) {
	scenario := struct {
		serverTime string
		body       string
		wantStatus int
		wantBody   string
		wantState  commandHistoryState
	}{
		serverTime: "0999-12-31T23:59:59Z",
		body:       `{"idempotency_key":"server-clock","amount_cents":1}`,
		wantStatus: 503,
		wantBody:   `{"error":{"code":"billing_unavailable","message":"Billing storage is unavailable. Retry the same operation identity and content."}}`,
		wantState:  commandHistoryState{creditIDs: "", balance: "0", version: 0, commands: 0},
	}
	pool := billingDatabase(t)
	serverTime := parseBillingTime(t, scenario.serverTime)
	handler := newBillingHandler(t, newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return serverTime }}), 5*time.Second, 8)

	response := financialRequest(t, handler, "POST", "/customers/acme/credits", scenario.body)

	if response.Code != scenario.wantStatus {
		t.Fatalf("POST /customers/acme/credits(%s clock=%s): status=%d body=%s; want %d", scenario.body, scenario.serverTime, response.Code, response.Body, scenario.wantStatus)
	}

	if response.Body.String() != scenario.wantBody+"\n" {
		t.Errorf("POST /customers/acme/credits(clock=%s): body=%s; want %s", scenario.serverTime, response.Body, scenario.wantBody)
	}

	assertCommandHistoryState(t, pool, scenario.wantState)
}
