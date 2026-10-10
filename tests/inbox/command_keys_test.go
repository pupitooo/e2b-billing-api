//go:build integration

package inbox_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type commandKeyState struct {
	creditEntries   int
	limitOperations int
	creditTicks     string
	version         int64
	limit           *int64
}

type commandKeyEndpoint struct {
	path        string
	body        string
	legacyBody  string
	wantSuccess commandKeyState
}

// testCommandBodyKeys checks exact JSON key names, types, token boundaries, and
// legacy transport rejection. Each case has an isolated schema and literal state.
func testCommandBodyKeys(t *testing.T, endpoint commandKeyEndpoint) {
	cases := []struct {
		name       string
		members    string
		body       string
		headerKey  string
		wantStatus int
		wantCode   string
		wantField  string
		wantState  commandKeyState
	}{
		{name: "missing", wantStatus: 422, wantCode: "invalid_command", wantField: "idempotency_key", wantState: commandKeyState{creditTicks: "0"}},
		{name: "header alone does not supply JSON key", headerKey: "header-only", wantStatus: 422, wantCode: "invalid_command", wantField: "idempotency_key", wantState: commandKeyState{creditTicks: "0"}},
		{name: "null", members: `"idempotency_key":null`, wantStatus: 422, wantCode: "invalid_command", wantField: "idempotency_key", wantState: commandKeyState{creditTicks: "0"}},
		{name: "number", members: `"idempotency_key":1`, wantStatus: 400, wantCode: "invalid_json", wantState: commandKeyState{creditTicks: "0"}},
		{name: "empty", members: `"idempotency_key":""`, wantStatus: 422, wantCode: "invalid_command", wantField: "idempotency_key", wantState: commandKeyState{creditTicks: "0"}},
		{name: "space", members: `"idempotency_key":"a b"`, wantStatus: 422, wantCode: "invalid_command", wantField: "idempotency_key", wantState: commandKeyState{creditTicks: "0"}},
		{name: "Unicode", members: `"idempotency_key":"ž"`, wantStatus: 422, wantCode: "invalid_command", wantField: "idempotency_key", wantState: commandKeyState{creditTicks: "0"}},
		{name: "duplicate fields", members: `"idempotency_key":"one","idempotency_key":"two"`, wantStatus: 400, wantCode: "invalid_json", wantState: commandKeyState{creditTicks: "0"}},
		{name: "case alias is rejected", members: `"Idempotency_Key":"one"`, wantStatus: 400, wantCode: "invalid_json", wantState: commandKeyState{creditTicks: "0"}},
		{name: "above maximum", members: `"idempotency_key":"` + strings.Repeat("a", 257) + `"`, wantStatus: 422, wantCode: "invalid_command", wantField: "idempotency_key", wantState: commandKeyState{creditTicks: "0"}},
		{name: "at maximum", members: `"idempotency_key":"` + strings.Repeat("a", 256) + `"`, wantStatus: 200, wantState: endpoint.wantSuccess},
		{name: "minimum", members: `"idempotency_key":"!"`, wantStatus: 200, wantState: endpoint.wantSuccess},
		{name: "body owns the key even when a header is present", members: `"idempotency_key":"body-key"`, headerKey: "different-header-key", wantStatus: 200, wantState: endpoint.wantSuccess},
		{name: "legacy operation_id is rejected", body: endpoint.legacyBody, wantStatus: 400, wantCode: "invalid_json", wantState: commandKeyState{creditTicks: "0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			body := endpoint.body
			if tc.body != "" {
				body = tc.body
			} else if tc.members != "" {
				body = `{` + tc.members + `,` + body[1:]
			}
			request := httptest.NewRequest(http.MethodPost, endpoint.path, strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			if tc.headerKey != "" {
				request.Header.Set("Idempotency-Key", tc.headerKey)
			}
			response := httptest.NewRecorder()

			resourceHandler(pool).ServeHTTP(response, request)

			if response.Code != tc.wantStatus {
				t.Fatalf("POST %s(%s) HTTP=%d body=%s; want %d", endpoint.path, body, response.Code, response.Body, tc.wantStatus)
			}
			var actual struct {
				Error struct {
					Code  string `json:"code"`
					Field string `json:"field"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
				t.Fatalf("Decode POST %s response: %v", endpoint.path, err)
			}

			if actual.Error.Code != tc.wantCode || actual.Error.Field != tc.wantField {
				t.Errorf("POST %s(%s) error=%+v; want code=%q field=%q", endpoint.path, body, actual.Error, tc.wantCode, tc.wantField)
			}

			var state commandKeyState
			err := pool.QueryRow(context.Background(), `SELECT
                (SELECT count(*) FROM credit_entries), (SELECT count(*) FROM spend_limit_operations),
                credit_balance_ticks::text,state_version,spend_limit_cents
                FROM customer_billing_state WHERE customer_id='acme'`).
				Scan(&state.creditEntries, &state.limitOperations, &state.creditTicks, &state.version, &state.limit)
			if err != nil {
				t.Fatalf("Read POST %s state: %v", endpoint.path, err)
			}

			if !reflect.DeepEqual(state, tc.wantState) {
				t.Errorf("POST %s(%s) state=%+v; want %+v", endpoint.path, body, state, tc.wantState)
			}
		})
	}
}
