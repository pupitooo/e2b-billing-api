//go:build integration

package inbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/postgres"

	"github.com/jackc/pgx/v5/pgxpool"
)

type commandHistoryState struct {
	creditIDs string
	balance   string
	limit     *int64
	version   int64
	commands  int
}

type financialCommandEndpoint struct {
	path               string
	scope              string
	key                string
	body               string
	changedBody        string
	wantState          commandHistoryState
	wantRecoveredState commandHistoryState
	wantChangedState   commandHistoryState
}

// TestAPICommandIdempotency verifies the shared history workflow for credit and
// limits: durable replay, conflicting content, atomic rollback, concurrency, and
// endpoint/customer scopes. Every case owns a private database schema.
func TestAPICommandIdempotency(t *testing.T) {
	endpoints := []struct {
		name string
		financialCommandEndpoint
	}{
		{
			name: "credit",
			financialCommandEndpoint: financialCommandEndpoint{
				path: "/customers/acme/credits", scope: "credits/acme", key: "ui-credit-request",
				body:               `{"amount_cents":1}`,
				changedBody:        `{"amount_cents":2}`,
				wantState:          commandHistoryState{creditIDs: "crd_1", balance: "1000000", version: 1, commands: 1},
				wantRecoveredState: commandHistoryState{creditIDs: "crd_2", balance: "1000000", version: 1, commands: 1},
				wantChangedState:   commandHistoryState{creditIDs: "crd_1", balance: "2000000", version: 1, commands: 1},
			},
		},
		{
			name: "spend limit",
			financialCommandEndpoint: financialCommandEndpoint{
				path: "/customers/acme/spend-limit", scope: "spend-limit/acme", key: "ui-limit-request",
				body: `{"limit_cents":1500}`, changedBody: `{"limit_cents":2000}`,
				wantState:          commandHistoryState{balance: "0", limit: limitValue(1_500), version: 1, commands: 1},
				wantRecoveredState: commandHistoryState{balance: "0", limit: limitValue(1_500), version: 1, commands: 1},
				wantChangedState:   commandHistoryState{balance: "0", limit: limitValue(2_000), version: 1, commands: 1},
			},
		},
		{
			name: "unlimited spend",
			financialCommandEndpoint: financialCommandEndpoint{
				path: "/customers/acme/spend-limit", scope: "spend-limit/acme", key: "ui-unlimited-request",
				body: `{"limit_cents":null}`, changedBody: `{"limit_cents":2000}`,
				wantState:          commandHistoryState{balance: "0", version: 1, commands: 1},
				wantRecoveredState: commandHistoryState{balance: "0", version: 1, commands: 1},
				wantChangedState:   commandHistoryState{balance: "0", limit: limitValue(2_000), version: 1, commands: 1},
			},
		},
	}
	for _, endpoint := range endpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			t.Run("durable replay", func(t *testing.T) { testCommandHistoryReplay(t, endpoint.financialCommandEndpoint) })
			t.Run("atomic rollback", func(t *testing.T) { testCommandHistoryRollback(t, endpoint.financialCommandEndpoint) })
			t.Run("concurrent requests", func(t *testing.T) { testCommandHistoryConcurrency(t, endpoint.financialCommandEndpoint) })
		})
	}
	t.Run("customer and endpoint scopes", testCommandHistoryScopes)
}

// testCommandHistoryReplay rebuilds the HTTP handler on each attempt. Original
// content replays the frozen result; changed content leaves the financial state
// and stored result intact. Credit IDs are generated independently of UI keys.
func testCommandHistoryReplay(t *testing.T, endpoint financialCommandEndpoint) {
	steps := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{name: "first request", body: endpoint.body, wantStatus: 200},
		{name: "retry through a new handler", body: endpoint.body, wantStatus: 200},
		{name: "changed content conflicts", body: endpoint.changedBody, wantStatus: 409, wantCode: "billing_conflict"},
		{name: "original still replays", body: endpoint.body, wantStatus: 200},
	}
	pool := billingDatabase(t)
	var originalResponse, originalResult string
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			response := financialRequest(t, resourceHandler(t, pool), http.MethodPost, endpoint.path, step.body, endpoint.key)

			if response.Code != step.wantStatus {
				t.Fatalf("POST %s key=%q body=%s: HTTP=%d response=%s; want %d", endpoint.path, endpoint.key, step.body, response.Code, response.Body, step.wantStatus)
			}
			var failure struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
				t.Fatal(err)
			}

			if failure.Error.Code != step.wantCode {
				t.Errorf("POST %s error=%q; want %q", endpoint.path, failure.Error.Code, step.wantCode)
			}
			if response.Code == http.StatusOK {
				if originalResponse == "" {
					originalResponse = response.Body.String()
				}
				if response.Body.String() != originalResponse {
					t.Errorf("POST %s replay=%s; want original %s", endpoint.path, response.Body, originalResponse)
				}
			}

			var storedResult string
			var storedKey string
			err := pool.QueryRow(context.Background(), `SELECT idempotency_key,response_payload::text
				FROM api_idempotency_operations WHERE operation_scope=$1 AND idempotency_key=$2`, endpoint.scope, endpoint.key).Scan(&storedKey, &storedResult)
			if err != nil {
				t.Fatal(err)
			}

			if storedKey != endpoint.key {
				t.Errorf("Stored key=%q; want original UI key %q", storedKey, endpoint.key)
			}
			if originalResult == "" {
				originalResult = storedResult
			}
			if storedResult != originalResult {
				t.Errorf("Stored result=%s; want frozen result %s", storedResult, originalResult)
			}
			if endpoint.wantState.creditIDs != "" {
				var result struct {
					CreditEntryID string `json:"credit_entry_id"`
				}
				if err := json.Unmarshal([]byte(storedResult), &result); err != nil {
					t.Fatal(err)
				}

				if result.CreditEntryID != endpoint.wantState.creditIDs {
					t.Errorf("Stored credit_entry_id=%q; want %q", result.CreditEntryID, endpoint.wantState.creditIDs)
				}
			}
			assertCommandHistoryState(t, pool, endpoint.wantState)
		})
	}
}

// testCommandHistoryRollback injects failure after financial writes but before
// the history row. Neither effect nor key survives; the same request can recover.
// A rolled-back credit sequence allocation leaves the expected harmless gap.
func testCommandHistoryRollback(t *testing.T, endpoint financialCommandEndpoint) {
	scenario := struct {
		setupSQL        string
		recoverySQL     string
		wantStatus      int
		wantCode        string
		wantFailed      commandHistoryState
		wantRecovered   commandHistoryState
		wantRetryStatus int
	}{
		setupSQL: `CREATE FUNCTION fail_command_history() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected history failure'; END $$;
			CREATE TRIGGER fail_command_history BEFORE INSERT ON api_idempotency_operations FOR EACH ROW EXECUTE FUNCTION fail_command_history()`,
		recoverySQL: "DROP TRIGGER fail_command_history ON api_idempotency_operations",
		wantStatus:  503, wantCode: "billing_unavailable", wantFailed: commandHistoryState{balance: "0"},
		wantRecovered: endpoint.wantRecoveredState, wantRetryStatus: 200,
	}
	pool := billingDatabase(t)
	if _, err := pool.Exec(context.Background(), scenario.setupSQL); err != nil {
		t.Fatal(err)
	}

	response := financialRequest(t, resourceHandler(t, pool), http.MethodPost, endpoint.path, endpoint.body, endpoint.key)

	if response.Code != scenario.wantStatus {
		t.Fatalf("POST %s history failure: HTTP=%d response=%s; want %d", endpoint.path, response.Code, response.Body, scenario.wantStatus)
	}
	var failure struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}

	if failure.Error.Code != scenario.wantCode {
		t.Errorf("POST %s failure=%q; want %q", endpoint.path, failure.Error.Code, scenario.wantCode)
	}
	assertCommandHistoryState(t, pool, scenario.wantFailed)

	if _, err := pool.Exec(context.Background(), scenario.recoverySQL); err != nil {
		t.Fatal(err)
	}

	retry := financialRequest(t, resourceHandler(t, pool), http.MethodPost, endpoint.path, endpoint.body, endpoint.key)

	if retry.Code != scenario.wantRetryStatus {
		t.Fatalf("POST %s after rollback: HTTP=%d response=%s; want %d", endpoint.path, retry.Code, retry.Body, scenario.wantRetryStatus)
	}
	assertCommandHistoryState(t, pool, scenario.wantRecovered)
}

// testCommandHistoryConcurrency starts independent HTTP requests together in a
// private schema. Equal inputs share one effect; unequal inputs have one winner.
func testCommandHistoryConcurrency(t *testing.T, endpoint financialCommandEndpoint) {
	cases := []struct {
		name             string
		bodies           []string
		wantSuccesses    int
		wantConflicts    int
		wantConflictCode string
		wantChangedState commandHistoryState
	}{
		{name: "identical inputs", bodies: []string{endpoint.body, endpoint.body}, wantSuccesses: 2},
		{name: "conflicting inputs", bodies: []string{endpoint.body, endpoint.changedBody}, wantSuccesses: 1, wantConflicts: 1, wantConflictCode: "billing_conflict", wantChangedState: endpoint.wantChangedState},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			start := make(chan struct{})
			type observed struct {
				status int
				body   string
			}
			results := make(chan observed, len(tc.bodies))
			var writers sync.WaitGroup
			for _, body := range tc.bodies {
				writers.Add(1)
				go func() {
					defer writers.Done()
					<-start
					response := financialRequest(t, resourceHandler(t, pool), http.MethodPost, endpoint.path, body, endpoint.key)
					results <- observed{status: response.Code, body: response.Body.String()}
				}()
			}
			close(start)
			writers.Wait()
			close(results)

			var successes, conflicts int
			var original string
			for result := range results {
				switch result.status {
				case http.StatusOK:
					successes++
					if original != "" && original != result.body {
						t.Errorf("Concurrent POST %s response=%s; want original %s", endpoint.path, result.body, original)
					}
					original = result.body
				case http.StatusConflict:
					conflicts++
					var failure struct {
						Error struct {
							Code string `json:"code"`
						} `json:"error"`
					}
					if err := json.Unmarshal([]byte(result.body), &failure); err != nil {
						t.Fatal(err)
					}

					if failure.Error.Code != tc.wantConflictCode {
						t.Errorf("Concurrent POST %s error=%q; want %q", endpoint.path, failure.Error.Code, tc.wantConflictCode)
					}
				default:
					t.Errorf("Concurrent POST %s HTTP=%d body=%s; want 200 or 409", endpoint.path, result.status, result.body)
				}
			}
			if successes != tc.wantSuccesses || conflicts != tc.wantConflicts {
				t.Fatalf("Concurrent POST %s successes=%d conflicts=%d; want %d %d", endpoint.path, successes, conflicts, tc.wantSuccesses, tc.wantConflicts)
			}

			var successful struct {
				AmountCents int64  `json:"amount_cents"`
				LimitCents  *int64 `json:"limit_cents"`
			}
			if err := json.Unmarshal([]byte(original), &successful); err != nil {
				t.Fatal(err)
			}
			want := endpoint.wantState
			if successful.AmountCents == 2 {
				want = tc.wantChangedState
			}
			if successful.LimitCents != nil && *successful.LimitCents == 2_000 {
				want = tc.wantChangedState
			}
			assertCommandHistoryState(t, pool, want)
		})
	}
}

// testCommandHistoryScopes uses one key across customers and all command types.
// One operation per scope persists while ledger IDs come from one server sequence.
func testCommandHistoryScopes(t *testing.T) {
	steps := []struct {
		name       string
		path       string
		body       string
		wantStatus int
	}{
		{name: "first customer grant", path: "/customers/acme/credits", body: `{"amount_cents":1}`, wantStatus: 200},
		{name: "second customer grant", path: "/customers/cyberdyne/credits", body: `{"amount_cents":1}`, wantStatus: 200},
		{name: "limit shares the UI key", path: "/customers/acme/spend-limit", body: `{"limit_cents":1500}`, wantStatus: 200},
		{name: "add-on shares the UI key", path: "/customers/acme/addons", body: `{"addon_name":"concurrency_pack","purchased_at":"2026-10-01"}`, wantStatus: 200},
		{name: "price shares the UI key", path: "/prices", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":"2026-12-01"}`, wantStatus: 200},
	}
	scenario := struct {
		key         string
		wantScopes  string
		wantIDs     string
		wantBalance string
		wantLimit   int64
		wantVersion int64
	}{
		key: "shared-ui-key", wantScopes: "addons/acme,credits/acme,credits/cyberdyne,prices,spend-limit/acme",
		wantIDs: "crd_1,crd_2", wantBalance: "1000000", wantLimit: 1_500, wantVersion: 3,
	}
	pool := billingDatabase(t)
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			response := financialRequest(t, resourceHandler(t, pool), http.MethodPost, step.path, step.body, scenario.key)

			if response.Code != step.wantStatus {
				t.Fatalf("POST %s key=%q: HTTP=%d body=%s; want %d", step.path, scenario.key, response.Code, response.Body, step.wantStatus)
			}
		})
	}

	var scopes, ids, balance string
	var limit, version int64
	err := pool.QueryRow(context.Background(), `SELECT
		(SELECT string_agg(operation_scope,',' ORDER BY operation_scope) FROM api_idempotency_operations WHERE idempotency_key=$1),
		(SELECT string_agg(credit_entry_id,',' ORDER BY credit_entry_id) FROM credit_entries),
		credit_balance_ticks::text,spend_limit_cents,state_version FROM customer_billing_state WHERE customer_id='acme'`, scenario.key).Scan(&scopes, &ids, &balance, &limit, &version)
	if err != nil {
		t.Fatal(err)
	}

	if scopes != scenario.wantScopes || ids != scenario.wantIDs || balance != scenario.wantBalance || limit != scenario.wantLimit || version != scenario.wantVersion {
		t.Errorf("Shared key state scopes=%s IDs=%s balance=%s limit=%d version=%d; want %+v", scopes, ids, balance, limit, version, scenario)
	}
}

// assertCommandHistoryState reads the ledger, account, and unified command count
// together so a replay or failed transaction cannot hide partial financial effects.
func assertCommandHistoryState(t *testing.T, pool *pgxpool.Pool, want commandHistoryState) {
	t.Helper()
	var actual commandHistoryState
	err := pool.QueryRow(context.Background(), `SELECT
		(SELECT COALESCE(string_agg(credit_entry_id,',' ORDER BY credit_entry_id),'') FROM credit_entries),
		credit_balance_ticks::text,spend_limit_cents,state_version,
		(SELECT count(*) FROM api_idempotency_operations)
		FROM customer_billing_state WHERE customer_id='acme'`).Scan(&actual.creditIDs, &actual.balance, &actual.limit, &actual.version, &actual.commands)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(actual, want) {
		t.Errorf("Command history and financial state=%+v; want %+v", actual, want)
	}
}

// testCreditGrantTimestampIdentity records the server clock once, then proves that
// retries with a later clock retain the original ledger and command audit times.
func testCreditGrantTimestampIdentity(t *testing.T) {
	steps := []struct {
		name        string
		serverTime  string
		amountCents int64
		wantError   error
	}{
		{name: "original grant normalizes UTC and truncates nanoseconds", serverTime: "2026-10-01T08:00:00.123456789+08:00", amountCents: 1},
		{name: "later retry retains the original server timestamp", serverTime: "2026-11-01T00:00:00Z", amountCents: 1},
		{name: "changed amount conflicts without a financial effect", serverTime: "2026-12-01T00:00:00Z", amountCents: 2, wantError: billing.ErrConflict},
	}
	scenario := struct {
		customer       string
		key            string
		wantRecordedAt string
		wantState      commandHistoryState
	}{customer: "acme", key: "timestamp-grant", wantRecordedAt: "2026-10-01T00:00:00.123456Z",
		wantState: commandHistoryState{creditIDs: "crd_1", balance: "1000000", version: 1, commands: 1}}
	pool := billingDatabase(t)
	var serverTime time.Time
	store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return serverTime }})
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			serverTime = parseBillingTime(t, step.serverTime)
			grant := billing.CreditGrant{IdempotencyKey: scenario.key, AmountCents: step.amountCents}

			err := store.GrantCredit(context.Background(), scenario.customer, grant)

			if !errors.Is(err, step.wantError) {
				t.Fatalf("GrantCredit(customer=%s grant=%+v clock=%s): error=%v; want %v", scenario.customer, grant, step.serverTime, err, step.wantError)
			}

			var recordedAt, operationAt time.Time
			if err := pool.QueryRow(context.Background(), `SELECT recorded_at,
                (SELECT created_at FROM api_idempotency_operations WHERE operation_scope='credits/acme' AND idempotency_key=$2)
                FROM credit_entries WHERE credit_entry_id=$1`, scenario.wantState.creditIDs, scenario.key).Scan(&recordedAt, &operationAt); err != nil {
				t.Fatal(err)
			}

			wantTime := parseBillingTime(t, scenario.wantRecordedAt)
			if !recordedAt.Equal(wantTime) || !operationAt.Equal(wantTime) {
				t.Errorf("GrantCredit(clock=%s): ledger time=%s command time=%s; want both %s", step.serverTime, recordedAt, operationAt, scenario.wantRecordedAt)
			}

			assertCommandHistoryState(t, pool, scenario.wantState)
		})
	}
}
