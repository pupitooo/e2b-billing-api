//go:build integration

package inbox_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/httpapi"
	"e2b/billing-api/internal/inbox"
)

// TestPriceAPI sends explicit wire commands through the real router and private
// PostgreSQL store; status, resolved times, and errors are declared beside each request.
// Every scenario owns a seeded schema and cannot affect application accounts.
func TestPriceAPI(t *testing.T) {
	t.Run("request key scopes", testResourceKeyScopes)
	t.Run("resource idempotency", func(t *testing.T) {
		testResourceIdempotency(t, resourceEndpoint{
			path: "/prices", idField: "price_version_id",
			body:        `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":null}`,
			changedBody: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":8,"effective_from":null}`,
			legacyBody:  `{"price_version_id":"caller-id","customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":null}`,
			wantPrices:  4, wantSubscriptions: 0, wantVersion: 0,
		})
	})
	t.Run("automatic activation retries", testPriceAutomaticRetries)
	cases := []struct {
		name              string
		body              string
		key               string
		serverTime        string
		wantEffectiveFrom string
		wantStatus        int
		wantCode          string
		wantField         string
		wantVersions      int
		wantRatings       int
		wantCreditEntries int
	}{
		{key: "price-command", name: "past price is rejected", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":"2026-10-31T23:59:59.999999Z"}`, wantStatus: 422, wantCode: "invalid_command", wantField: "effective_from", wantVersions: 3, wantRatings: 0, wantCreditEntries: 0},
		{key: "price-command", name: "exact insertion instant is accepted", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":"2026-11-01T00:00:00Z"}`, wantStatus: 200, wantVersions: 4, wantRatings: 0, wantCreditEntries: 0},
		{key: "price-command", name: "future default", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":"2026-12-01T00:00:00Z"}`, wantStatus: 200, wantVersions: 4, wantRatings: 0, wantCreditEntries: 0},
		{key: "price-command", name: "customer override", body: `{"customer_id":"acme","metric":"cpu_seconds","price_per_million_cents":3,"effective_from":"2026-12-01T00:00:00Z"}`, wantStatus: 200, wantVersions: 4, wantRatings: 0, wantCreditEntries: 0},
		{key: "price-command", name: "explicit zero price", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":0,"effective_from":"2026-12-01T00:00:00Z"}`, wantStatus: 200, wantVersions: 4, wantRatings: 0, wantCreditEntries: 0},
		{key: "price-command", name: "missing cents", body: `{"customer_id":null,"metric":"cpu_seconds","effective_from":"2026-12-01T00:00:00Z"}`, wantStatus: 422, wantCode: "invalid_command", wantField: "price_per_million_cents", wantVersions: 3, wantRatings: 0, wantCreditEntries: 0},
		{key: "price-command", name: "null customer differs from empty", body: `{"customer_id":"","metric":"cpu_seconds","price_per_million_cents":7,"effective_from":"2026-12-01T00:00:00Z"}`, wantStatus: 422, wantCode: "invalid_command", wantField: "customer_id", wantVersions: 3, wantRatings: 0, wantCreditEntries: 0},
		{key: "price-command", name: "duplicate field", body: `{"customer_id":null,"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":"2026-12-01T00:00:00Z"}`, wantStatus: 400, wantCode: "invalid_json", wantVersions: 3, wantRatings: 0, wantCreditEntries: 0},
		{key: "price-command", name: "invalid timestamp", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":"2026-12-01"}`, wantStatus: 422, wantCode: "invalid_command", wantField: "effective_from", wantVersions: 3, wantRatings: 0, wantCreditEntries: 0},
		{key: "price-command", name: "null uses current server time", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":5,"effective_from":null}`, wantStatus: 200, wantEffectiveFrom: "2026-11-01T00:00:00Z", wantVersions: 4, wantRatings: 0, wantCreditEntries: 0},
		{key: "price-command", name: "null truncates server nanoseconds to database precision", serverTime: "2026-11-01T08:00:00.123456789+08:00", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":null}`, wantStatus: 200, wantEffectiveFrom: "2026-11-01T00:00:00.123456Z", wantVersions: 4},
		{key: "price-command", name: "null customer price", body: `{"customer_id":"acme","metric":"cpu_seconds","price_per_million_cents":7,"effective_from":null}`, wantStatus: 200, wantEffectiveFrom: "2026-11-01T00:00:00Z", wantVersions: 4},
		{key: "price-command", name: "null does not bypass negative price validation", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":-1,"effective_from":null}`, wantStatus: 422, wantCode: "invalid_command", wantField: "price_per_million_cents", wantVersions: 3},
		{key: "price-command", name: "null requires an existing customer", body: `{"customer_id":"missing","metric":"cpu_seconds","price_per_million_cents":7,"effective_from":null}`, wantStatus: 404, wantCode: "not_found", wantVersions: 3},
		{key: "price-command", name: "null requires an existing metric", body: `{"customer_id":null,"metric":"missing","price_per_million_cents":7,"effective_from":null}`, wantStatus: 404, wantCode: "not_found", wantVersions: 3},
		{key: "price-command", name: "null still conflicts at an occupied activation instant", serverTime: "2026-10-15T00:00:00Z", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":null}`, wantStatus: 409, wantCode: "billing_conflict", wantVersions: 3},
		{key: "price-command", name: "empty effective start is rejected", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":""}`, wantStatus: 422, wantCode: "invalid_command", wantField: "effective_from", wantVersions: 3},
		{key: "price-command", name: "numeric effective start is rejected", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":1}`, wantStatus: 400, wantCode: "invalid_json", wantVersions: 3},
		{key: "price-command", name: "effective start must be supplied", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":5}`, wantStatus: 422, wantCode: "invalid_command", wantField: "effective_from", wantVersions: 3, wantRatings: 0, wantCreditEntries: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			serverTimeText := tc.serverTime
			if serverTimeText == "" {
				serverTimeText = "2026-11-01T00:00:00Z"
			}
			serverTime := parseBillingTime(t, serverTimeText)
			handler := httpapi.NewHandler(inbox.NewPostgres(pool, time.Second), 5*time.Second, 8, billing.NewStoreWithClock(pool, func() time.Time { return serverTime }))
			response := financialRequest(t, handler, http.MethodPost, "/prices", tc.body, tc.key)
			if response.Code != tc.wantStatus {
				t.Fatalf("POST /prices(%s): status=%d body=%s want %d", tc.body, response.Code, response.Body, tc.wantStatus)
			}
			if tc.wantCode != "" {
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
					t.Fatalf("POST /prices: error=%+v want code=%s field=%s", actual.Error, tc.wantCode, tc.wantField)
				}
			}
			if tc.wantEffectiveFrom != "" {
				var actual struct {
					ID            string `json:"price_version_id"`
					EffectiveFrom string `json:"effective_from"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
					t.Fatal(err)
				}
				if actual.EffectiveFrom != tc.wantEffectiveFrom {
					t.Errorf("POST /prices(%s): effective_from=%s; want %s", tc.body, actual.EffectiveFrom, tc.wantEffectiveFrom)
				}

				var stored time.Time
				if err := pool.QueryRow(context.Background(), "SELECT effective_from FROM price_versions WHERE price_version_id=$1", actual.ID).Scan(&stored); err != nil {
					t.Fatal(err)
				}
				if got := stored.UTC().Format(time.RFC3339Nano); got != tc.wantEffectiveFrom {
					t.Errorf("POST /prices(%s): stored effective_from=%s; want %s", tc.body, got, tc.wantEffectiveFrom)
				}
			}
			var versions, ratings, creditEntries int
			if err := pool.QueryRow(context.Background(), "SELECT (SELECT count(*) FROM price_versions),(SELECT count(*) FROM usage_ratings),(SELECT count(*) FROM credit_entries)").Scan(&versions, &ratings, &creditEntries); err != nil {
				t.Fatal(err)
			}
			if versions != tc.wantVersions || ratings != tc.wantRatings || creditEntries != tc.wantCreditEntries {
				t.Errorf("POST /prices(%s): versions=%d ratings=%d credit entries=%d; want %d,%d,%d", tc.body, versions, ratings, creditEntries, tc.wantVersions, tc.wantRatings, tc.wantCreditEntries)
			}
		})
	}
}

// financialHandler owns a private seeded schema for one HTTP scenario; it never
// changes application accounts or depends on a separately running API process.
func financialHandler(t testing.TB) http.Handler {
	t.Helper()
	pool := billingDatabase(t)
	return httpapi.NewHandler(inbox.NewPostgres(pool, time.Second), 5*time.Second, 8, billing.NewStore(pool))
}

// financialRequest preserves explicit command parameters and inserts only the
// scenario's declared JSON keys, including duplicates for transport rejection.
func financialRequest(t testing.TB, handler http.Handler, method, path, body string, keys ...string) *httptest.ResponseRecorder {
	t.Helper()
	body = financialCommandBody(t, body, keys...)
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// financialCommandBody keeps the caller's raw object intact while adding named
// key values; it never chooses an identity or repairs ambiguous JSON fixtures.
func financialCommandBody(t testing.TB, body string, keys ...string) string {
	t.Helper()
	for _, key := range keys {
		encoded, err := json.Marshal(key)
		if err != nil {
			t.Fatalf("Encode idempotency_key=%q: %v", key, err)
		}
		body = `{"idempotency_key":` + string(encoded) + `,` + body[1:]
	}
	return body
}

// testPriceAutomaticRetries executes declared HTTP steps in one private schema
// per scenario. Retries preserve the timestamp, row count, and financial state.
func testPriceAutomaticRetries(t *testing.T) {
	type step struct {
		name              string
		serverTime        string
		body              string
		key               string
		wantStatus        int
		wantCode          string
		wantEffectiveFrom string
	}
	cases := []struct {
		name           string
		steps          []step
		wantStoredTime string
		wantVersions   int
	}{
		{name: "null retry retains original time", steps: []step{
			{key: "price-command", name: "insert", serverTime: "2026-11-01T00:00:00Z", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":null}`, wantStatus: 200, wantEffectiveFrom: "2026-11-01T00:00:00Z"},
			{key: "price-command", name: "retry after activation", serverTime: "2026-12-01T00:00:00Z", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":null}`, wantStatus: 200, wantEffectiveFrom: "2026-11-01T00:00:00Z"},
		}, wantStoredTime: "2026-11-01T00:00:00Z", wantVersions: 4},
		{name: "changed amount conflicts", steps: []step{
			{key: "price-command", name: "insert", serverTime: "2026-11-01T00:00:00Z", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":null}`, wantStatus: 200, wantEffectiveFrom: "2026-11-01T00:00:00Z"},
			{key: "price-command", name: "changed retry", serverTime: "2026-12-01T00:00:00Z", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":8,"effective_from":null}`, wantStatus: 409, wantCode: "billing_conflict"},
		}, wantStoredTime: "2026-11-01T00:00:00Z", wantVersions: 4},
		{name: "changed customer conflicts", steps: []step{
			{key: "price-command", name: "insert", serverTime: "2026-11-01T00:00:00Z", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":null}`, wantStatus: 200, wantEffectiveFrom: "2026-11-01T00:00:00Z"},
			{key: "price-command", name: "changed retry", serverTime: "2026-12-01T00:00:00Z", body: `{"customer_id":"acme","metric":"cpu_seconds","price_per_million_cents":7,"effective_from":null}`, wantStatus: 409, wantCode: "billing_conflict"},
		}, wantStoredTime: "2026-11-01T00:00:00Z", wantVersions: 4},
		{name: "changed metric conflicts", steps: []step{
			{key: "price-command", name: "insert", serverTime: "2026-11-01T00:00:00Z", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":null}`, wantStatus: 200, wantEffectiveFrom: "2026-11-01T00:00:00Z"},
			{key: "price-command", name: "changed retry", serverTime: "2026-12-01T00:00:00Z", body: `{"customer_id":null,"metric":"other","price_per_million_cents":7,"effective_from":null}`, wantStatus: 409, wantCode: "billing_conflict"},
		}, wantStoredTime: "2026-11-01T00:00:00Z", wantVersions: 4},
		{name: "explicit retry matches resolved timestamp", steps: []step{
			{key: "price-command", name: "insert", serverTime: "2026-11-01T00:00:00Z", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":null}`, wantStatus: 200, wantEffectiveFrom: "2026-11-01T00:00:00Z"},
			{key: "price-command", name: "explicit retry", serverTime: "2026-12-01T00:00:00Z", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":"2026-11-01T08:00:00+08:00"}`, wantStatus: 200, wantEffectiveFrom: "2026-11-01T00:00:00Z"},
		}, wantStoredTime: "2026-11-01T00:00:00Z", wantVersions: 4},
		{name: "changed explicit timestamp conflicts", steps: []step{
			{key: "price-command", name: "insert", serverTime: "2026-11-01T00:00:00Z", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":null}`, wantStatus: 200, wantEffectiveFrom: "2026-11-01T00:00:00Z"},
			{key: "price-command", name: "changed retry", serverTime: "2026-12-01T00:00:00Z", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":"2026-11-01T00:00:00.000001Z"}`, wantStatus: 409, wantCode: "billing_conflict"},
		}, wantStoredTime: "2026-11-01T00:00:00Z", wantVersions: 4},
		{name: "null reuses explicitly scheduled timestamp", steps: []step{
			{key: "price-command", name: "schedule", serverTime: "2026-11-01T00:00:00Z", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":"2026-12-01T00:00:00Z"}`, wantStatus: 200, wantEffectiveFrom: "2026-12-01T00:00:00Z"},
			{key: "price-command", name: "null retry", serverTime: "2027-01-01T00:00:00Z", body: `{"customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":null}`, wantStatus: 200, wantEffectiveFrom: "2026-12-01T00:00:00Z"},
		}, wantStoredTime: "2026-12-01T00:00:00Z", wantVersions: 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			var serverTime time.Time
			handler := httpapi.NewHandler(inbox.NewPostgres(pool, time.Second), 5*time.Second, 8, billing.NewStoreWithClock(pool, func() time.Time { return serverTime }))
			before, err := readPriceBoundaryState(ctx, pool, "cyberdyne")
			if err != nil {
				t.Fatal(err)
			}

			for _, input := range tc.steps {
				t.Run(input.name, func(t *testing.T) {
					serverTime = parseBillingTime(t, input.serverTime)
					response := financialRequest(t, handler, http.MethodPost, "/prices", input.body, input.key)

					if response.Code != input.wantStatus {
						t.Fatalf("POST /prices(%s, now=%s): status=%d body=%s; want %d", input.body, input.serverTime, response.Code, response.Body, input.wantStatus)
					}
					var actual struct {
						EffectiveFrom string `json:"effective_from"`
						Error         struct {
							Code string `json:"code"`
						} `json:"error"`
					}
					if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
						t.Fatal(err)
					}
					if actual.Error.Code != input.wantCode {
						t.Fatalf("POST /prices(%s): error code=%s; want %s", input.body, actual.Error.Code, input.wantCode)
					}

					if actual.EffectiveFrom != input.wantEffectiveFrom {
						t.Errorf("POST /prices(%s): effective_from=%s; want %s", input.body, actual.EffectiveFrom, input.wantEffectiveFrom)
					}
				})
			}

			var stored time.Time
			var versions int
			if err := pool.QueryRow(ctx, "SELECT effective_from, (SELECT count(*) FROM price_versions) FROM price_versions WHERE price_version_id=(SELECT response_payload->>'ID' FROM api_idempotency_operations WHERE operation_scope='prices' AND idempotency_key='price-command')").Scan(&stored, &versions); err != nil {
				t.Fatal(err)
			}
			if got := stored.UTC().Format(time.RFC3339Nano); got != tc.wantStoredTime || versions != tc.wantVersions {
				t.Errorf("POST /prices workflow %s: stored time=%s versions=%d; want %s and %d", tc.name, got, versions, tc.wantStoredTime, tc.wantVersions)
			}

			after, err := readPriceBoundaryState(ctx, pool, "cyberdyne")
			if err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Errorf("POST /prices workflow %s: financial state=%+v; want unchanged %+v", tc.name, after, before)
			}
		})
	}
}
