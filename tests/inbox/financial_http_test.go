//go:build integration

package inbox_test

import (
	"bytes"
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
// PostgreSQL store; status and error fields are declared beside each request.
func TestPriceAPI(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
		wantField  string
	}{
		{name: "future default", body: `{"price_version_id":"future","customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":"2026-12-01T00:00:00Z"}`, wantStatus: 200},
		{name: "customer override", body: `{"price_version_id":"future","customer_id":"acme","metric":"cpu_seconds","price_per_million_cents":3,"effective_from":"2026-12-01T00:00:00Z"}`, wantStatus: 200},
		{name: "explicit zero price", body: `{"price_version_id":"free","customer_id":null,"metric":"cpu_seconds","price_per_million_cents":0,"effective_from":"2026-12-01T00:00:00Z"}`, wantStatus: 200},
		{name: "missing cents", body: `{"price_version_id":"future","customer_id":null,"metric":"cpu_seconds","effective_from":"2026-12-01T00:00:00Z"}`, wantStatus: 422, wantCode: "invalid_command", wantField: "price_per_million_cents"},
		{name: "null customer differs from empty", body: `{"price_version_id":"future","customer_id":"","metric":"cpu_seconds","price_per_million_cents":7,"effective_from":"2026-12-01T00:00:00Z"}`, wantStatus: 422, wantCode: "invalid_command", wantField: "customer_id"},
		{name: "duplicate field", body: `{"price_version_id":"one","price_version_id":"two","customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":"2026-12-01T00:00:00Z"}`, wantStatus: 400, wantCode: "invalid_json"},
		{name: "invalid timestamp", body: `{"price_version_id":"future","customer_id":null,"metric":"cpu_seconds","price_per_million_cents":7,"effective_from":"2026-12-01"}`, wantStatus: 422, wantCode: "invalid_command", wantField: "effective_from"},
		{name: "undated baseline is rejected", body: `{"price_version_id":"baseline","customer_id":null,"metric":"cpu_seconds","price_per_million_cents":5,"effective_from":null}`, wantStatus: 422, wantCode: "invalid_command", wantField: "effective_from"},
		{name: "effective start must be supplied", body: `{"price_version_id":"baseline","customer_id":null,"metric":"cpu_seconds","price_per_million_cents":5}`, wantStatus: 422, wantCode: "invalid_command", wantField: "effective_from"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := financialHandler(t)
			response := financialRequest(t, handler, http.MethodPost, "/prices", tc.body)
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

// financialRequest preserves the scenario's method, URL, and literal JSON while
// supplying only the common transport header and an in-memory response recorder.
func financialRequest(t testing.TB, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
