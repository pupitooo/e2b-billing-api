//go:build integration

package inbox_test

import (
	"context"
	"e2b/billing-api/internal/postgres"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

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
			store := newPostgresStore(t, pool, postgres.Config{})
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
			if actual.LimitReached != tc.wantReached || actual.GrossChargeMicrocents != tc.wantGross {
				t.Fatalf("MonthlyLimitStatus(month=%s limit=%v gross=%s): %+v want reached=%t gross=%s", tc.month, tc.limit, tc.gross, actual, tc.wantReached, tc.wantGross)
			}
		})
	}
}

// TestSetSpendLimit distinguishes retries from new changes, including returning
// to a previous amount, and checks conflicts without unintended state changes.
// Limit-operation timestamps use the shared store clock in UTC microseconds.
func TestSetSpendLimit(t *testing.T) {
	t.Run("shared storage clock timestamps limit operation", func(t *testing.T) {
		scenario := struct {
			customer       string
			change         billing.SpendLimitChange
			clockTime      time.Time
			wantError      error
			wantRecordedAt time.Time
			wantLimit      int64
			wantVersion    int64
			wantOperations int
		}{
			customer:       "cyberdyne",
			change:         billing.SpendLimitChange{IdempotencyKey: "clocked-limit", LimitCents: limitValue(1_500)},
			clockTime:      time.Date(2026, 10, 11, 8, 0, 0, 123_456_789, time.FixedZone("UTC+08", 8*60*60)),
			wantError:      nil,
			wantRecordedAt: time.Date(2026, 10, 11, 0, 0, 0, 123_456_000, time.UTC),
			wantLimit:      1_500, wantVersion: 1, wantOperations: 1,
		}
		pool := billingDatabase(t)
		store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return scenario.clockTime }})
		ctx := context.Background()

		err := store.SetSpendLimit(ctx, scenario.customer, scenario.change)

		if !errors.Is(err, scenario.wantError) {
			t.Fatalf("SetSpendLimit(customer=%s, change=%+v, clock=%s): error=%v; want %v", scenario.customer, scenario.change, scenario.clockTime, err, scenario.wantError)
		}

		var recorded time.Time
		var limit, version int64
		var operations int
		err = pool.QueryRow(ctx, `SELECT o.created_at, s.spend_limit_cents, s.state_version,
   (SELECT count(*) FROM api_idempotency_operations WHERE operation_scope='spend-limit/' || $1)
   FROM api_idempotency_operations o JOIN customer_billing_state s ON o.operation_scope='spend-limit/' || s.customer_id
   WHERE o.operation_scope='spend-limit/' || $1 AND o.idempotency_key=$2`, scenario.customer, scenario.change.IdempotencyKey).Scan(&recorded, &limit, &version, &operations)
		if err != nil {
			t.Fatalf("Read limit operation(customer=%s, key=%s): %v", scenario.customer, scenario.change.IdempotencyKey, err)
		}

		if !recorded.Equal(scenario.wantRecordedAt) {
			t.Errorf("SetSpendLimit(clock=%s): recorded_at=%s; want %s", scenario.clockTime, recorded, scenario.wantRecordedAt)
		}

		if limit != scenario.wantLimit || version != scenario.wantVersion || operations != scenario.wantOperations {
			t.Errorf("SetSpendLimit(customer=%s, change=%+v): limit=%d version=%d operations=%d; want %d %d %d", scenario.customer, scenario.change, limit, version, operations, scenario.wantLimit, scenario.wantVersion, scenario.wantOperations)
		}
	})
	t.Run("old replay preserves new limit", func(t *testing.T) {
		scenario := struct {
			changes     []billing.SpendLimitChange
			wantLimit   int64
			wantVersion int64
		}{changes: []billing.SpendLimitChange{{IdempotencyKey: "first", LimitCents: limitValue(1500)}, {IdempotencyKey: "raised", LimitCents: limitValue(2000)}, {IdempotencyKey: "first", LimitCents: limitValue(1500)}}, wantLimit: 2000, wantVersion: 2}
		store := newPostgresStore(t, billingDatabase(t), postgres.Config{})
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
			store := newPostgresStore(t, pool, postgres.Config{})
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
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM api_idempotency_operations WHERE operation_scope='spend-limit/' || $1", tc.customer).Scan(&operations); err != nil {
				t.Fatalf("Count spend-limit operations(customer=%s) error=%v; want nil", tc.customer, err)
			}

			if operations != tc.wantOperations {
				t.Errorf("SetSpendLimit(customer=%s changes=%+v) operations=%d; want %d", tc.customer, tc.changes, operations, tc.wantOperations)
			}
		})
	}
}

// TestSpendLimitAPI verifies response fields, persisted nullable configuration,
// exact gross thresholds and both sides of the current UTC month boundary.
func TestSpendLimitAPI(t *testing.T) {
	t.Run("JSON idempotency keys", func(t *testing.T) {
		testCommandBodyKeys(t, commandKeyEndpoint{
			path:        "/customers/acme/spend-limit",
			body:        `{"limit_cents":1500}`,
			legacyBody:  `{"operation_id":"legacy","limit_cents":1500}`,
			wantSuccess: commandKeyState{limitOperations: 1, creditMicrocents: "0", version: 1, limit: limitValue(1500)},
		})
	})

	scenario := struct {
		initialLimit             int64
		initialGross, serverTime string
	}{initialLimit: 1501, initialGross: "1500000000", serverTime: "2026-11-01T07:59:59+08:00"}
	cases := []struct {
		name, method, path, body, wantCode, wantField, wantBody string
		serverTime                                              string
		wantStatus                                              int
		wantLimit                                               *int64
		wantVersion                                             int64
		wantOperations                                          int
		wantReached                                             bool
	}{
		{name: "set finite limit at exact gross boundary", method: "POST", path: "/customers/cyberdyne/spend-limit", body: `{"idempotency_key":"limit","limit_cents":1500}`, wantStatus: 200, wantBody: `{"idempotency_key":"limit","limit_cents":1500}`, wantLimit: limitValue(1500), wantVersion: 2, wantOperations: 2, wantReached: true},
		{name: "remove limit explicitly", method: "POST", path: "/customers/cyberdyne/spend-limit", body: `{"idempotency_key":"unlimited","limit_cents":null}`, wantStatus: 200, wantBody: `{"idempotency_key":"unlimited","limit_cents":null}`, wantVersion: 2, wantOperations: 2},
		{name: "omitted limit preserves finite configuration", method: "POST", path: "/customers/cyberdyne/spend-limit", body: `{"idempotency_key":"limit"}`, wantStatus: 422, wantCode: "invalid_command", wantField: "limit_cents", wantLimit: limitValue(1501), wantVersion: 1, wantOperations: 1},
		{name: "negative limit preserves finite configuration", method: "POST", path: "/customers/cyberdyne/spend-limit", body: `{"idempotency_key":"limit","limit_cents":-1}`, wantStatus: 422, wantCode: "invalid_command", wantField: "limit_cents", wantLimit: limitValue(1501), wantVersion: 1, wantOperations: 1},
		{name: "zero limit is valid and reached", method: "POST", path: "/customers/cyberdyne/spend-limit", body: `{"idempotency_key":"limit","limit_cents":0}`, wantStatus: 200, wantBody: `{"idempotency_key":"limit","limit_cents":0}`, wantLimit: limitValue(0), wantVersion: 2, wantOperations: 2, wantReached: true},
		{name: "read monthly status", method: "GET", path: "/customers/cyberdyne/months/2026-10/limit-status", wantStatus: 200, wantBody: `{"customer_id":"cyberdyne","month":"2026-10","limit_cents":1501,"gross_charge_microcents":"1500000000","limit_reached":false,"state_version":1,"pending_events":0,"processing_errors":0}`, wantLimit: limitValue(1501), wantVersion: 1, wantOperations: 1},
		{name: "noncanonical month rejected", method: "GET", path: "/customers/cyberdyne/months/2026-1/limit-status", wantStatus: 422, wantCode: "invalid_command", wantField: "month", wantLimit: limitValue(1501), wantVersion: 1, wantOperations: 1},
		{name: "current UTC status before boundary despite November local date", method: "GET", path: "/customers/cyberdyne/limit-status", wantStatus: 200, wantBody: `{"customer_id":"cyberdyne","month":"2026-10","limit_cents":1501,"gross_charge_microcents":"1500000000","limit_reached":false,"state_version":1,"pending_events":0,"processing_errors":0}`, wantLimit: limitValue(1501), wantVersion: 1, wantOperations: 1},
		{name: "current UTC status at November boundary resets gross", serverTime: "2026-11-01T08:00:00+08:00", method: "GET", path: "/customers/cyberdyne/limit-status", wantStatus: 200, wantBody: `{"customer_id":"cyberdyne","month":"2026-11","limit_cents":1501,"gross_charge_microcents":"0","limit_reached":false,"state_version":1,"pending_events":0,"processing_errors":0}`, wantLimit: limitValue(1501), wantVersion: 1, wantOperations: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			clockText := scenario.serverTime
			if tc.serverTime != "" {
				clockText = tc.serverTime
			}
			clock := parseBillingTime(t, clockText)
			store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return clock }})
			if err := store.SetSpendLimit(ctx, "cyberdyne", billing.SpendLimitChange{IdempotencyKey: "initial", LimitCents: &scenario.initialLimit}); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "INSERT INTO monthly_usage VALUES ('cyberdyne','2026-10-01',$1::numeric)", scenario.initialGross); err != nil {
				t.Fatal(err)
			}
			handler := newBillingHandlerWithClock(t, store, 5*time.Second, 8, func() time.Time { return clock })
			before := addonFinancialSnapshot(t, pool)

			response := financialRequest(t, handler, tc.method, tc.path, tc.body)

			if response.Code != tc.wantStatus {
				t.Fatalf("%s %s(%s): HTTP=%d body=%s; want %d", tc.method, tc.path, tc.body, response.Code, response.Body, tc.wantStatus)
			}
			var failure struct {
				Error struct{ Code, Field string } `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
				t.Fatal(err)
			}
			if failure.Error.Code != tc.wantCode || failure.Error.Field != tc.wantField {
				t.Fatalf("%s %s: error=%+v; want code=%s field=%s", tc.method, tc.path, failure.Error, tc.wantCode, tc.wantField)
			}
			if tc.wantBody != "" && response.Body.String() != tc.wantBody+"\n" {
				t.Errorf("%s %s: body=%s; want %s", tc.method, tc.path, response.Body, tc.wantBody)
			}
			if tc.wantStatus != 200 || tc.method == "GET" {
				if after := addonFinancialSnapshot(t, pool); after != before {
					t.Fatalf("Read/rejected limit changed financial state: before=%s after=%s", before, after)
				}
			}

			var limit *int64
			var version int64
			var operations int
			if err := pool.QueryRow(ctx, `SELECT spend_limit_cents,state_version,(SELECT count(*) FROM api_idempotency_operations) FROM customer_billing_state WHERE customer_id='cyberdyne'`).Scan(&limit, &version, &operations); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(limit, tc.wantLimit) || version != tc.wantVersion || operations != tc.wantOperations {
				t.Fatalf("%s %s: limit=%v version=%d operations=%d; want %v %d %d", tc.method, tc.path, limit, version, operations, tc.wantLimit, tc.wantVersion, tc.wantOperations)
			}
			if tc.method == "POST" && tc.wantStatus == 200 {
				status, err := store.MonthlyLimitStatus(ctx, "cyberdyne", "2026-10")
				if err != nil {
					t.Fatal(err)
				}
				if status.LimitReached != tc.wantReached || status.GrossChargeMicrocents != scenario.initialGross || !reflect.DeepEqual(status.LimitCents, tc.wantLimit) {
					t.Fatalf("Stored October status=%+v; want limit=%v reached=%t gross=%s", status, tc.wantLimit, tc.wantReached, scenario.initialGross)
				}
			}
		})
	}

}

// limitValue supplies an address for the literal cents in a scenario; it does
// not transform money, select limits, or calculate expected outcomes.
func limitValue(cents int64) *int64 { return &cents }
