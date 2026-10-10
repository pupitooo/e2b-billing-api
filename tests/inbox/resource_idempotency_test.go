//go:build integration

package inbox_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/httpapi"
	"e2b/billing-api/internal/inbox"
	"github.com/jackc/pgx/v5/pgxpool"
)

type resourceEndpoint struct {
	path              string
	body              string
	changedBody       string
	idField           string
	legacyBody        string
	wantPrices        int
	wantSubscriptions int
	wantVersion       int64
}

type resourceState struct {
	prices        int
	subscriptions int
	operations    int
	stateVersion  int64
	creditTicks   string
	ratings       int
}

// testResourceIdempotency runs transport, durable replay, rollback, and races
// under the endpoint's top-level test. Each subtest owns a private seeded schema.
func testResourceIdempotency(t *testing.T, endpoint resourceEndpoint) {
	t.Run("key validation", func(t *testing.T) { testResourceBodyValidation(t, endpoint) })
	t.Run("durable result replay", func(t *testing.T) { testResourceDurableReplay(t, endpoint) })
	t.Run("transaction rollback", func(t *testing.T) { testResourceOperationRollback(t, endpoint) })
	t.Run("concurrent requests", func(t *testing.T) { testResourceConcurrentRequests(t, endpoint) })
}

// testResourceBodyValidation checks missing, duplicate, and invalid JSON keys,
// both sides of the byte boundary, and rejection of client resource IDs.
func testResourceBodyValidation(t *testing.T, endpoint resourceEndpoint) {
	cases := []struct {
		name       string
		keys       []string
		body       string
		headerKey  string
		wantStatus int
		wantCode   string
		wantField  string
		wantState  resourceState
	}{
		{name: "missing", wantStatus: 422, wantCode: "invalid_command", wantField: "idempotency_key", wantState: resourceState{prices: 3, creditTicks: "0"}},
		{name: "header alone does not supply the JSON key", headerKey: "old-header", wantStatus: 422, wantCode: "invalid_command", wantField: "idempotency_key", wantState: resourceState{prices: 3, creditTicks: "0"}},
		{name: "null", body: `{"idempotency_key":null,` + endpoint.body[1:], wantStatus: 422, wantCode: "invalid_command", wantField: "idempotency_key", wantState: resourceState{prices: 3, creditTicks: "0"}},
		{name: "numeric key", body: `{"idempotency_key":1,` + endpoint.body[1:], wantStatus: 400, wantCode: "invalid_json", wantState: resourceState{prices: 3, creditTicks: "0"}},
		{name: "boolean key", body: `{"idempotency_key":true,` + endpoint.body[1:], wantStatus: 400, wantCode: "invalid_json", wantState: resourceState{prices: 3, creditTicks: "0"}},
		{name: "case alias", body: `{"Idempotency_Key":"key",` + endpoint.body[1:], wantStatus: 400, wantCode: "invalid_json", wantState: resourceState{prices: 3, creditTicks: "0"}},
		{name: "empty", keys: []string{""}, wantStatus: 422, wantCode: "invalid_command", wantField: "idempotency_key", wantState: resourceState{prices: 3, creditTicks: "0"}},
		{name: "space", keys: []string{"a b"}, wantStatus: 422, wantCode: "invalid_command", wantField: "idempotency_key", wantState: resourceState{prices: 3, creditTicks: "0"}},
		{name: "tab", keys: []string{"a\tb"}, wantStatus: 422, wantCode: "invalid_command", wantField: "idempotency_key", wantState: resourceState{prices: 3, creditTicks: "0"}},
		{name: "Unicode", keys: []string{"ž"}, wantStatus: 422, wantCode: "invalid_command", wantField: "idempotency_key", wantState: resourceState{prices: 3, creditTicks: "0"}},
		{name: "repeated equal values", keys: []string{"one", "one"}, wantStatus: 400, wantCode: "invalid_json", wantState: resourceState{prices: 3, creditTicks: "0"}},
		{name: "repeated unequal values", keys: []string{"one", "two"}, wantStatus: 400, wantCode: "invalid_json", wantState: resourceState{prices: 3, creditTicks: "0"}},
		{name: "above byte limit", keys: []string{strings.Repeat("a", 257)}, wantStatus: 422, wantCode: "invalid_command", wantField: "idempotency_key", wantState: resourceState{prices: 3, creditTicks: "0"}},
		{name: "at byte limit", keys: []string{strings.Repeat("a", 256)}, wantStatus: 200, wantState: resourceState{prices: endpoint.wantPrices, subscriptions: endpoint.wantSubscriptions, operations: 1, stateVersion: endpoint.wantVersion, creditTicks: "0"}},
		{name: "single character", keys: []string{"!"}, wantStatus: 200, wantState: resourceState{prices: endpoint.wantPrices, subscriptions: endpoint.wantSubscriptions, operations: 1, stateVersion: endpoint.wantVersion, creditTicks: "0"}},
		{name: "body key is authoritative", keys: []string{"body-key"}, headerKey: "different-header-key", wantStatus: 200, wantState: resourceState{prices: endpoint.wantPrices, subscriptions: endpoint.wantSubscriptions, operations: 1, stateVersion: endpoint.wantVersion, creditTicks: "0"}},
		{name: "caller resource ID rejected", keys: []string{"legacy"}, body: endpoint.legacyBody, wantStatus: 400, wantCode: "invalid_json", wantState: resourceState{prices: 3, creditTicks: "0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			handler := resourceHandler(pool)
			body := tc.body
			if body == "" {
				body = endpoint.body
			}

			request := httptest.NewRequest(http.MethodPost, endpoint.path, strings.NewReader(financialCommandBody(t, body, tc.keys...)))
			request.Header.Set("Content-Type", "application/json")
			if tc.headerKey != "" {
				request.Header.Set("Idempotency-Key", tc.headerKey)
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != tc.wantStatus {
				t.Fatalf("POST %s key=%q body=%s: HTTP=%d response=%s; want %d", endpoint.path, tc.keys, body, response.Code, response.Body, tc.wantStatus)
			}
			var actual struct {
				Error struct {
					Code  string `json:"code"`
					Field string `json:"field"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
				t.Fatal(err)
			}
			if actual.Error.Code != tc.wantCode || actual.Error.Field != tc.wantField {
				t.Errorf("POST %s key=%q: error=%+v; want code=%q field=%q", endpoint.path, tc.keys, actual.Error, tc.wantCode, tc.wantField)
			}
			assertResourceState(t, pool, tc.wantState)
		})
	}
}

// testResourceDurableReplay reconstructs the HTTP handler for each attempt,
// proving the database owns identity and conflict detection after response loss.
func testResourceDurableReplay(t *testing.T, endpoint resourceEndpoint) {
	steps := []struct {
		name             string
		key              string
		body             string
		wantStatus       int
		wantCode         string
		wantSameResponse bool
	}{
		{name: "create", key: "durable-command", body: endpoint.body, wantStatus: 200},
		{name: "lost reply retry through a new handler", key: "durable-command", body: endpoint.body, wantStatus: 200, wantSameResponse: true},
		{name: "changed content", key: "durable-command", body: endpoint.changedBody, wantStatus: 409, wantCode: "billing_conflict"},
		{name: "original content still replays", key: "durable-command", body: endpoint.body, wantStatus: 200, wantSameResponse: true},
	}
	wantState := resourceState{prices: endpoint.wantPrices, subscriptions: endpoint.wantSubscriptions, operations: 1, stateVersion: endpoint.wantVersion, creditTicks: "0"}
	pool := billingDatabase(t)
	var originalResponse, originalID string
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			response := financialRequest(t, resourceHandler(pool), http.MethodPost, endpoint.path, step.body, step.key)

			if response.Code != step.wantStatus {
				t.Fatalf("POST %s key=%q body=%s: HTTP=%d response=%s; want %d", endpoint.path, step.key, step.body, response.Code, response.Body, step.wantStatus)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &fields); err != nil {
				t.Fatal(err)
			}
			if step.wantCode != "" {
				var failure struct {
					Code string `json:"code"`
				}
				if err := json.Unmarshal(fields["error"], &failure); err != nil {
					t.Fatal(err)
				}
				if failure.Code != step.wantCode {
					t.Errorf("error code=%s; want %s", failure.Code, step.wantCode)
				}
			} else {
				var id string
				if err := json.Unmarshal(fields[endpoint.idField], &id); err != nil {
					t.Fatal(err)
				}
				if id == "" || id == step.key {
					t.Fatalf("%s=%q; want generated identity distinct from key %q", endpoint.idField, id, step.key)
				}
				if originalID == "" {
					originalID, originalResponse = id, response.Body.String()
				}
				if step.wantSameResponse && (id != originalID || response.Body.String() != originalResponse) {
					t.Errorf("replay response=%s; want original %s", response.Body, originalResponse)
				}
			}
			assertResourceState(t, pool, wantState)
		})
	}
	var storedResponse []byte
	var storedRequestObject bool
	if err := pool.QueryRow(context.Background(), `SELECT response_payload, jsonb_typeof(request_payload)='object'
        FROM api_idempotency_operations WHERE idempotency_key='durable-command'`).Scan(&storedResponse, &storedRequestObject); err != nil {
		t.Fatal(err)
	}
	if !storedRequestObject {
		t.Error("stored request is not an object; want original normalized command")
	}
	if !strings.Contains(string(storedResponse), originalID) {
		t.Errorf("stored result=%s; want generated ID %q", storedResponse, originalID)
	}
}

// testResourceOperationRollback fails the operation-history insertion after the
// financial write. The effect and key roll back together, and retry can succeed.
func testResourceOperationRollback(t *testing.T, endpoint resourceEndpoint) {
	scenario := struct {
		key               string
		body              string
		setupSQL          string
		recoverySQL       string
		wantFailureStatus int
		wantFailureCode   string
		wantFailedState   resourceState
		wantRetryStatus   int
		wantRetriedState  resourceState
	}{key: "rollback-command", body: endpoint.body,
		setupSQL: `CREATE FUNCTION fail_operation_record() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected operation persistence failure'; END $$;
            CREATE TRIGGER fail_operation_record BEFORE INSERT ON api_idempotency_operations FOR EACH ROW EXECUTE FUNCTION fail_operation_record()`,
		recoverySQL:       "DROP TRIGGER fail_operation_record ON api_idempotency_operations",
		wantFailureStatus: 503, wantFailureCode: "billing_unavailable", wantFailedState: resourceState{prices: 3, creditTicks: "0"},
		wantRetryStatus: 200, wantRetriedState: resourceState{prices: endpoint.wantPrices, subscriptions: endpoint.wantSubscriptions, operations: 1, stateVersion: endpoint.wantVersion, creditTicks: "0"}}
	pool := billingDatabase(t)
	if _, err := pool.Exec(context.Background(), scenario.setupSQL); err != nil {
		t.Fatal(err)
	}

	response := financialRequest(t, resourceHandler(pool), http.MethodPost, endpoint.path, scenario.body, scenario.key)

	if response.Code != scenario.wantFailureStatus {
		t.Fatalf("injected operation failure: HTTP=%d body=%s; want %d", response.Code, response.Body, scenario.wantFailureStatus)
	}
	var failure struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != scenario.wantFailureCode {
		t.Errorf("failure code=%q; want %q", failure.Error.Code, scenario.wantFailureCode)
	}
	assertResourceState(t, pool, scenario.wantFailedState)

	if _, err := pool.Exec(context.Background(), scenario.recoverySQL); err != nil {
		t.Fatal(err)
	}
	retry := financialRequest(t, resourceHandler(pool), http.MethodPost, endpoint.path, scenario.body, scenario.key)

	if retry.Code != scenario.wantRetryStatus {
		t.Fatalf("retry after rollback: HTTP=%d body=%s; want %d", retry.Code, retry.Body, scenario.wantRetryStatus)
	}
	assertResourceState(t, pool, scenario.wantRetriedState)
}

// testResourceConcurrentRequests starts two independent HTTP requests together.
// Equal commands share a result; unequal commands have exactly one winner.
func testResourceConcurrentRequests(t *testing.T, endpoint resourceEndpoint) {
	cases := []struct {
		name          string
		key           string
		bodies        []string
		wantSuccesses int
		wantConflicts int
		wantState     resourceState
	}{
		{name: "identical", key: "concurrent", bodies: []string{endpoint.body, endpoint.body}, wantSuccesses: 2, wantState: resourceState{prices: endpoint.wantPrices, subscriptions: endpoint.wantSubscriptions, operations: 1, stateVersion: endpoint.wantVersion, creditTicks: "0"}},
		{name: "changed content", key: "concurrent", bodies: []string{endpoint.body, endpoint.changedBody}, wantSuccesses: 1, wantConflicts: 1, wantState: resourceState{prices: endpoint.wantPrices, subscriptions: endpoint.wantSubscriptions, operations: 1, stateVersion: endpoint.wantVersion, creditTicks: "0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			handler := resourceHandler(pool)
			type observed struct {
				status int
				body   string
			}
			responses := make(chan observed, len(tc.bodies))
			start := make(chan struct{})
			var writers sync.WaitGroup
			for _, body := range tc.bodies {
				writers.Add(1)
				go func() {
					defer writers.Done()
					<-start
					body = financialCommandBody(t, body, tc.key)
					request := httptest.NewRequest(http.MethodPost, endpoint.path, strings.NewReader(body))
					request.Header.Set("Content-Type", "application/json")
					response := httptest.NewRecorder()
					handler.ServeHTTP(response, request)
					responses <- observed{status: response.Code, body: response.Body.String()}
				}()
			}
			close(start)
			writers.Wait()
			close(responses)

			var successes, conflicts int
			var firstResponse string
			for response := range responses {
				switch response.status {
				case 200:
					successes++
					if firstResponse != "" && response.body != firstResponse {
						t.Errorf("concurrent result=%s; want original %s", response.body, firstResponse)
					}
					firstResponse = response.body
				case 409:
					conflicts++
					var failure struct {
						Error struct {
							Code string `json:"code"`
						} `json:"error"`
					}
					if err := json.Unmarshal([]byte(response.body), &failure); err != nil {
						t.Fatal(err)
					}
					if failure.Error.Code != "billing_conflict" {
						t.Errorf("concurrent error=%q; want billing_conflict", failure.Error.Code)
					}
				default:
					t.Errorf("concurrent POST %s: HTTP=%d response=%s; want success or conflict", endpoint.path, response.status, response.body)
				}
			}
			if successes != tc.wantSuccesses || conflicts != tc.wantConflicts {
				t.Errorf("concurrent requests: successes=%d conflicts=%d; want %d and %d", successes, conflicts, tc.wantSuccesses, tc.wantConflicts)
			}
			assertResourceState(t, pool, tc.wantState)
		})
	}
}

// resourceHandler wires the real router to the caller's private schema and a
// fixed insertion clock; rebuilding it cannot retain any in-memory request keys.
func resourceHandler(pool *pgxpool.Pool) http.Handler {
	return httpapi.NewHandler(inbox.NewPostgres(pool, time.Second), 5*time.Second, 8,
		billing.NewStoreWithClock(pool, func() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) }))
}

// assertResourceState checks committed resources, durable keys, and independent
// account and usage effects against explicit expected state. The seeded acme
// account is the default; identity cases select their declared customer.
func assertResourceState(t *testing.T, pool *pgxpool.Pool, want resourceState, customers ...string) {
	t.Helper()
	customer := "acme"
	if len(customers) > 0 {
		customer = customers[0]
	}

	var got resourceState
	err := pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM price_versions),
        (SELECT count(*) FROM addon_subscriptions), (SELECT count(*) FROM api_idempotency_operations),
        state_version,credit_balance_ticks::text,(SELECT count(*) FROM usage_ratings)
        FROM customer_billing_state WHERE customer_id=$1`, customer).
		Scan(&got.prices, &got.subscriptions, &got.operations, &got.stateVersion, &got.creditTicks, &got.ratings)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("committed resource/account state(customer=%q)=%+v; want %+v", customer, got, want)
	}
}

// testResourceKeyScopes uses one key for two endpoints and two customers. Each
// scope owns its own result, while billing still generates distinct resource IDs.
func testResourceKeyScopes(t *testing.T) {
	scenario := struct {
		key   string
		steps []struct {
			path       string
			body       string
			idField    string
			wantStatus int
		}
		wantState resourceState
	}{key: "shared-key", steps: []struct {
		path       string
		body       string
		idField    string
		wantStatus int
	}{
		{path: "/prices", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":null}`, idField: "price_version_id", wantStatus: 200},
		{path: "/customers/acme/addons", body: `{"addon_name":"concurrency_pack","purchased_at":"2026-10-05T00:00:00Z"}`, idField: "subscription_id", wantStatus: 200},
		{path: "/customers/cyberdyne/addons", body: `{"addon_name":"concurrency_pack","purchased_at":"2026-10-05T00:00:00Z"}`, idField: "subscription_id", wantStatus: 200},
	}, wantState: resourceState{prices: 4, subscriptions: 2, operations: 3, stateVersion: 1, creditTicks: "0"}}
	pool := billingDatabase(t)
	seen := map[string]bool{}
	for _, step := range scenario.steps {
		response := financialRequest(t, resourceHandler(pool), http.MethodPost, step.path, step.body, scenario.key)
		if response.Code != step.wantStatus {
			t.Fatalf("POST %s key=%q: HTTP=%d body=%s; want %d", step.path, scenario.key, response.Code, response.Body, step.wantStatus)
		}
		var encoded map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &encoded); err != nil {
			t.Fatal(err)
		}
		var id string
		if err := json.Unmarshal(encoded[step.idField], &id); err != nil {
			t.Fatal(err)
		}
		if id == "" || seen[id] {
			t.Fatalf("POST %s generated ID=%q; want a new distinct resource ID", step.path, id)
		}
		seen[id] = true
	}
	assertResourceState(t, pool, scenario.wantState)
}

// testAddonReplayAfterClosing preserves the generated subscription and purchased
// price after a catalog update and invoice publication through a new store.
func testAddonReplayAfterClosing(t *testing.T) {
	scenario := struct {
		customer         string
		key              string
		purchase         billing.AddonPurchase
		catalogUpdateSQL string
		invoiceMonth     string
		wantInvoiceCents int64
		wantMonthlyPrice int64
		wantState        resourceState
	}{customer: "acme", key: "original-purchase",
		purchase:         billing.AddonPurchase{AddonName: "concurrency_pack", PurchasedAt: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)},
		catalogUpdateSQL: "UPDATE addons SET monthly_price_cents=3000 WHERE addon_name='concurrency_pack'",
		invoiceMonth:     "2026-10", wantInvoiceCents: 2000, wantMonthlyPrice: 2000,
		wantState: resourceState{prices: 3, subscriptions: 1, operations: 1, stateVersion: 2, creditTicks: "0"}}
	pool := billingDatabase(t)
	store := billing.NewStoreWithClock(pool, func() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) })
	original, err := store.PurchaseAddon(context.Background(), scenario.customer, scenario.key, scenario.purchase)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), scenario.catalogUpdateSQL); err != nil {
		t.Fatal(err)
	}
	invoice, err := store.CloseMonth(context.Background(), scenario.customer, scenario.invoiceMonth)
	if err != nil {
		t.Fatal(err)
	}
	if invoice.TotalCents != scenario.wantInvoiceCents {
		t.Errorf("invoice total=%d; want %d", invoice.TotalCents, scenario.wantInvoiceCents)
	}

	replay, err := billing.NewStore(pool).PurchaseAddon(context.Background(), scenario.customer, scenario.key, scenario.purchase)

	if err != nil {
		t.Fatal(err)
	}
	if replay != original {
		t.Errorf("subscription replay=%+v; want original %+v", replay, original)
	}
	if replay.MonthlyPriceCents != scenario.wantMonthlyPrice {
		t.Errorf("replayed price=%d; want %d", replay.MonthlyPriceCents, scenario.wantMonthlyPrice)
	}
	assertResourceState(t, pool, scenario.wantState)
}
