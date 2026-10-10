package httpapi_test

import (
	"context"
	"e2b/billing-api/internal/httpapi"
	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/usage"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Handler.ServeHTTP routes requests, validates complete batches, and returns
// the declared HTTP result after storage commits or reports a retryable failure.
func TestHandlerServeHTTP(t *testing.T) {
	t.Run("handler routes", func(t *testing.T) {
		tests := []struct {
			name        string
			description string
			method      string
			path        string
			wantStatus  int
			wantAllow   string
			body        string
		}{
			{
				name:        "health",
				description: "The health endpoint responds successfully to GET requests.",
				method:      http.MethodGet,
				path:        "/healthz",
				wantStatus:  http.StatusOK,
				wantAllow:   "",
				body:        "",
			},
			{
				name:        "usage batch",
				description: "The usage endpoint acknowledges a valid POST request with HTTP 202.",
				method:      http.MethodPost,
				path:        "/usage/batches",
				wantStatus:  http.StatusAccepted,
				wantAllow:   "",
				body:        validUsageBatch,
			},
			{
				name:        "unknown route",
				description: "An unregistered path returns HTTP 404.",
				method:      http.MethodGet,
				path:        "/missing",
				wantStatus:  http.StatusNotFound,
				wantAllow:   "",
				body:        "",
			},
			{
				name:        "usage method",
				description: "The usage endpoint rejects GET requests and advertises POST in the Allow header.",
				method:      http.MethodGet,
				path:        "/usage/batches",
				wantStatus:  http.StatusMethodNotAllowed,
				wantAllow:   "POST",
				body:        "",
			},
		}
		handler := httpapi.NewHandler(acceptingStore(), 10*time.Second, 32)
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Log(tt.description)
				response := httptest.NewRecorder()
				request := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
				request.Header.Set("Content-Type", "application/json")
				handler.ServeHTTP(response, request)
				if response.Code != tt.wantStatus {
					t.Fatalf("Status = %d, want %d", response.Code, tt.wantStatus)
				}

				if tt.wantAllow != "" && response.Header().Get("Allow") != tt.wantAllow {
					t.Errorf("Allow = %q, want %q", response.Header().Get("Allow"), tt.wantAllow)
				}
			})
		}
	})
	t.Run("usage batch response", func(t *testing.T) {
		tt := struct {
			body            string
			wantStatus      int
			wantContentType string
			wantBodyStatus  string
		}{
			body:            validUsageBatch,
			wantStatus:      http.StatusAccepted,
			wantContentType: "application/json",
			wantBodyStatus:  "accepted",
		}

		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(tt.body))
		request.Header.Set("Content-Type", "application/json")
		httpapi.NewHandler(acceptingStore(), 10*time.Second, 32).ServeHTTP(response, request)
		if response.Code != tt.wantStatus {
			t.Fatalf("Status = %d, want %d", response.Code, http.StatusAccepted)
		}

		if got := response.Header().Get("Content-Type"); got != tt.wantContentType {
			t.Errorf("Content-Type = %q, want application/json", got)
		}

		var body struct {
			Status string `json:"status"`
		}
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatalf("Decode response: %v", err)
		}

		if body.Status != tt.wantBodyStatus {
			t.Errorf("Response status = %q, want accepted", body.Status)
		}
	})
	t.Run("usage batch valid input", func(t *testing.T) {
		tests := []struct {
			name       string
			body       string
			wantStatus int
		}{
			{
				name:       "assignment input",
				body:       validUsageBatch,
				wantStatus: http.StatusAccepted,
			},
			{
				name:       "optional batch identifier",
				body:       changeBatch(t, func(b map[string]any) { delete(b, "batch_id") }),
				wantStatus: http.StatusAccepted,
			},
			{
				name:       "zero units",
				body:       changeEvent(t, func(e map[string]any) { e["units"] = 0 }),
				wantStatus: http.StatusAccepted,
			},
			{
				name:       "maximum units",
				body:       changeEvent(t, func(e map[string]any) { e["units"] = json.Number("9223372036854775807") }),
				wantStatus: http.StatusAccepted,
			},
			{
				name:       "maximum version",
				body:       changeEvent(t, func(e map[string]any) { e["schema_version"] = json.Number("2147483647") }),
				wantStatus: http.StatusAccepted,
			},
			{
				name:       "Unicode byte boundary",
				body:       changeEvent(t, func(e map[string]any) { e["source"] = strings.Repeat("ž", 128) }),
				wantStatus: http.StatusAccepted,
			},
			{
				name: "UTC month boundary",
				body: changeEvent(t, func(e map[string]any) {
					e["period_start"] = "2026-10-31T23:59:59.999999Z"
					e["period_end"] = "2026-11-01T08:00:00+08:00"
				}),
				wantStatus: http.StatusAccepted,
			},
			{
				name: "cross-month usage is accepted for asynchronous accounting",
				body: changeEvent(t, func(e map[string]any) {
					e["period_start"] = "2026-10-31T23:59:00Z"
					e["period_end"] = "2026-11-01T00:01:00Z"
				}),
				wantStatus: http.StatusAccepted,
			},
			{
				name: "offset wall clock comparison",
				body: changeEvent(t, func(e map[string]any) {
					e["period_start"] = "2026-10-10T12:00:00+02:00"
					e["period_end"] = "2026-10-10T11:00:00Z"
				}),
				wantStatus: http.StatusAccepted,
			},
			{
				name: "minimum UTC year",
				body: changeEvent(t, func(e map[string]any) {
					e["period_start"] = "1000-01-01T00:00:00Z"
					e["period_end"] = "1000-01-01T01:00:00Z"
				}),
				wantStatus: http.StatusAccepted,
			},
			{
				name: "local year below minimum becomes valid in UTC",
				body: changeEvent(t, func(e map[string]any) {
					e["period_start"] = "0999-12-31T23:30:00-01:00"
					e["period_end"] = "1000-01-01T01:30:00Z"
				}),
				wantStatus: http.StatusAccepted,
			},
			{
				name:       "batch identifier at the exact ASCII byte limit",
				body:       changeBatch(t, func(b map[string]any) { b["batch_id"] = strings.Repeat("x", 256) }),
				wantStatus: 202,
			},
			{
				name:       "batch identifier at the exact Unicode byte limit",
				body:       changeBatch(t, func(b map[string]any) { b["batch_id"] = strings.Repeat("ž", 128) }),
				wantStatus: 202,
			},
			{
				name:       "valid surrogate pair",
				body:       strings.Replace(validUsageBatch, "test-event", `\ud83d\ude80`, 1),
				wantStatus: http.StatusAccepted,
			},
			{
				name: "lowest valid surrogate pair",
				body: strings.Replace(validUsageBatch, "test-event", `\ud800\udc00`, 1), wantStatus: 202,
			},
			{
				name: "highest valid surrogate pair",
				body: strings.Replace(validUsageBatch, "test-event", `\udbff\udfff`, 1), wantStatus: 202,
			},
			{
				name:       "escaped literal surrogate text",
				body:       strings.Replace(validUsageBatch, "test-event", `\\ud800`, 1),
				wantStatus: http.StatusAccepted,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				response := postBatch(tt.body, "application/json")
				if response.Code != tt.wantStatus {
					t.Fatalf("Status = %d, want 202; response: %s", response.Code, response.Body)
				}
			})
		}
	})
	t.Run("usage batch required fields", func(t *testing.T) {
		fields := []struct {
			name           string
			wantErrorField string
		}{
			{
				name:           "source",
				wantErrorField: "events[0].source",
			},
			{
				name:           "event_id",
				wantErrorField: "events[0].event_id",
			},
			{
				name:           "schema_version",
				wantErrorField: "events[0].schema_version",
			},
			{
				name:           "customer_id",
				wantErrorField: "events[0].customer_id",
			},
			{
				name:           "sandbox_id",
				wantErrorField: "events[0].sandbox_id",
			},
			{
				name:           "metric",
				wantErrorField: "events[0].metric",
			},
			{
				name:           "period_start",
				wantErrorField: "events[0].period_start",
			},
			{
				name:           "period_end",
				wantErrorField: "events[0].period_end",
			},
			{
				name:           "units",
				wantErrorField: "events[0].units",
			},
		}
		inputs := []struct {
			name          string
			missing       bool
			wantStatus    int
			wantErrorCode string
		}{
			{
				name:          "missing",
				missing:       true,
				wantStatus:    422,
				wantErrorCode: "invalid_batch",
			},
			{
				name:          "null",
				missing:       false,
				wantStatus:    422,
				wantErrorCode: "invalid_batch",
			},
		}
		for _, field := range fields {
			t.Run(field.name, func(t *testing.T) {
				for _, tt := range inputs {
					t.Run(tt.name, func(t *testing.T) {
						body := changeEvent(t, func(event map[string]any) {
							if tt.missing {
								delete(event, field.name)
							} else {
								event[field.name] = nil
							}
						})
						response := postBatch(body, "application/json")
						assertRequestError(t, response, tt.wantStatus, tt.wantErrorCode, field.wantErrorField)
					})
				}
			})
		}
	})
	t.Run("usage batch invalid json", func(t *testing.T) {
		tests := []struct {
			name           string
			body           string
			wantErrorField string
			wantStatus     int
			wantErrorCode  string
		}{
			{
				name:           "empty body",
				body:           "",
				wantErrorField: "",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
			{
				name:           "truncated document",
				body:           `{"events":[`,
				wantErrorField: "",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
			{
				name:           "multiple documents",
				body:           validUsageBatch + `{}`,
				wantErrorField: "",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
			{
				name:           "top-level array",
				body:           `[]`,
				wantErrorField: "",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
			{
				name:           "top-level null",
				body:           `null`,
				wantErrorField: "",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
			{
				name:           "unknown envelope member",
				body:           changeBatch(t, func(b map[string]any) { b["extra"] = true }),
				wantErrorField: "",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
			{
				name:           "duplicate envelope member",
				body:           strings.Replace(validUsageBatch, `"batch_id":"test"`, `"batch_id":"test","batch_id":"other"`, 1),
				wantErrorField: "",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
			{
				name:           "case-aliased envelope member",
				body:           strings.Replace(validUsageBatch, `"events"`, `"Events"`, 1),
				wantErrorField: "",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
			{
				name:           "object instead of array",
				body:           `{"events":{}}`,
				wantErrorField: "events",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
			{
				name:           "null event",
				body:           `{"events":[null]}`,
				wantErrorField: "events[0]",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
			{
				name:           "unknown event member",
				body:           changeEvent(t, func(e map[string]any) { e["received_at"] = "2026-10-10T13:00:00Z" }),
				wantErrorField: "events[0]",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
			{
				name:           "duplicate event member",
				body:           strings.Replace(validUsageBatch, `"event_id":"test-event"`, `"event_id":"test-event","event_id":"other"`, 1),
				wantErrorField: "events[0]",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
			{
				name:           "case-aliased event member",
				body:           strings.Replace(validUsageBatch, `"event_id"`, `"EVENT_ID"`, 1),
				wantErrorField: "events[0]",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
			{
				name:           "numeric identifier",
				body:           changeEvent(t, func(e map[string]any) { e["source"] = 1 }),
				wantErrorField: "events[0]",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
			{
				name:           "fractional units",
				body:           changeEvent(t, func(e map[string]any) { e["units"] = 1.5 }),
				wantErrorField: "events[0]",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
			{
				name:           "exponent units",
				body:           changeEvent(t, func(e map[string]any) { e["units"] = json.Number("1e3") }),
				wantErrorField: "events[0]",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
			{
				name:           "units overflow",
				body:           changeEvent(t, func(e map[string]any) { e["units"] = json.Number("9223372036854775808") }),
				wantErrorField: "events[0]",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
			{
				name:           "version overflow",
				body:           changeEvent(t, func(e map[string]any) { e["schema_version"] = json.Number("2147483648") }),
				wantErrorField: "events[0]",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
			{
				name:           "invalid UTF-8",
				body:           strings.Replace(validUsageBatch, "test-event", "\xff", 1),
				wantErrorField: "",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
			{
				name:           "unpaired lowest low surrogate",
				body:           strings.Replace(validUsageBatch, "test-event", `\udc00`, 1),
				wantErrorField: "", wantStatus: 400, wantErrorCode: "invalid_json",
			},
			{
				name:           "unpaired highest high surrogate",
				body:           strings.Replace(validUsageBatch, "test-event", `\udbff`, 1),
				wantErrorField: "", wantStatus: 400, wantErrorCode: "invalid_json",
			},
			{
				name:           "unpaired high surrogate",
				body:           strings.Replace(validUsageBatch, "test-event", `\ud800`, 1),
				wantErrorField: "",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
			{
				name:           "unpaired low surrogate",
				body:           strings.Replace(validUsageBatch, "test-event", `\udfff`, 1),
				wantErrorField: "",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
			{
				name:           "high surrogate followed by ordinary escape",
				body:           strings.Replace(validUsageBatch, "test-event", `\ud800\u0041`, 1),
				wantErrorField: "",
				wantStatus:     400,
				wantErrorCode:  "invalid_json",
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				assertRequestError(t, postBatch(tt.body, "application/json"), tt.wantStatus, tt.wantErrorCode, tt.wantErrorField)
			})
		}
	})
	t.Run("usage batch invalid values", func(t *testing.T) {
		tests := []struct {
			name           string
			body           string
			wantErrorField string
			wantStatus     int
			wantErrorCode  string
		}{
			{
				name:           "missing events",
				body:           `{}`,
				wantErrorField: "events",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "null events",
				body:           `{"events":null}`,
				wantErrorField: "events",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "empty events",
				body:           `{"events":[]}`,
				wantErrorField: "events",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "null batch identifier",
				body:           changeBatch(t, func(b map[string]any) { b["batch_id"] = nil }),
				wantErrorField: "batch_id",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "blank batch identifier",
				body:           changeBatch(t, func(b map[string]any) { b["batch_id"] = "\t\n" }),
				wantErrorField: "batch_id",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "long batch identifier",
				body:           changeBatch(t, func(b map[string]any) { b["batch_id"] = strings.Repeat("x", 257) }),
				wantErrorField: "batch_id",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "NUL batch identifier",
				body:           changeBatch(t, func(b map[string]any) { b["batch_id"] = "a\x00b" }),
				wantErrorField: "batch_id",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "Unicode whitespace identifier",
				body:           changeEvent(t, func(e map[string]any) { e["source"] = "\t\u00a0\u2003\n" }),
				wantErrorField: "events[0].source",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "identifier exceeds UTF-8 byte limit",
				body:           changeEvent(t, func(e map[string]any) { e["source"] = strings.Repeat("ž", 129) }),
				wantErrorField: "events[0].source",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "NUL identifier",
				body:           changeEvent(t, func(e map[string]any) { e["event_id"] = "a\x00b" }),
				wantErrorField: "events[0].event_id",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "zero schema version",
				body:           changeEvent(t, func(e map[string]any) { e["schema_version"] = 0 }),
				wantErrorField: "events[0].schema_version",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "negative units",
				body:           changeEvent(t, func(e map[string]any) { e["units"] = -1 }),
				wantErrorField: "events[0].units",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "infinite timestamp",
				body:           changeEvent(t, func(e map[string]any) { e["period_end"] = "infinity" }),
				wantErrorField: "events[0].period_end",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "missing timestamp offset",
				body:           changeEvent(t, func(e map[string]any) { e["period_start"] = "2026-10-10T12:00:00" }),
				wantErrorField: "events[0].period_start",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "invalid offset hour",
				body:           changeEvent(t, func(e map[string]any) { e["period_start"] = "2026-10-10T12:00:00+24:00" }),
				wantErrorField: "events[0].period_start",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "invalid offset minute",
				body:           changeEvent(t, func(e map[string]any) { e["period_start"] = "2026-10-10T12:00:00+00:60" }),
				wantErrorField: "events[0].period_start",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "invalid calendar date",
				body:           changeEvent(t, func(e map[string]any) { e["period_start"] = "2026-02-30T12:00:00Z" }),
				wantErrorField: "events[0].period_start",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "submicrosecond timestamp",
				body:           changeEvent(t, func(e map[string]any) { e["period_start"] = "2026-10-10T12:00:00.0000001Z" }),
				wantErrorField: "events[0].period_start",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "start below minimum UTC year",
				body:           changeEvent(t, func(e map[string]any) { e["period_start"] = "0999-12-31T23:59:59Z" }),
				wantErrorField: "events[0].period_start",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "end below minimum UTC year",
				body:           changeEvent(t, func(e map[string]any) { e["period_end"] = "0999-12-31T23:59:59Z" }),
				wantErrorField: "events[0].period_end",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "offset moves start below minimum UTC year",
				body:           changeEvent(t, func(e map[string]any) { e["period_start"] = "1000-01-01T00:00:00+01:00" }),
				wantErrorField: "events[0].period_start",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "equal instants in different offsets",
				body:           changeEvent(t, func(e map[string]any) { e["period_end"] = "2026-10-10T20:00:00+08:00" }),
				wantErrorField: "events[0].period_end",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
			{
				name:           "end before start",
				body:           changeEvent(t, func(e map[string]any) { e["period_end"] = "2026-10-10T11:59:59Z" }),
				wantErrorField: "events[0].period_end",
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				assertRequestError(t, postBatch(tt.body, "application/json"), tt.wantStatus, tt.wantErrorCode, tt.wantErrorField)
			})
		}
	})
	t.Run("usage batch limits", func(t *testing.T) {
		tests := []struct {
			name           string
			body           string
			wantStatus     int
			wantErrorCode  string
			wantErrorField string
		}{
			{
				name:       "body at one MiB",
				body:       validUsageBatch + strings.Repeat(" ", (1<<20)-len(validUsageBatch)),
				wantStatus: 202,
			},
			{
				name:          "body one byte over one MiB",
				body:          validUsageBatch + strings.Repeat(" ", (1<<20)-len(validUsageBatch)+1),
				wantStatus:    413,
				wantErrorCode: "request_too_large",
			},
			{
				name:       "one thousand events",
				body:       repeatedEvents(t, 1_000),
				wantStatus: 202,
			},
			{
				name:           "one thousand and one events",
				body:           repeatedEvents(t, 1_001),
				wantStatus:     422,
				wantErrorCode:  "invalid_batch",
				wantErrorField: "events",
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				response := postBatch(tt.body, "application/json")
				if tt.wantErrorCode != "" {
					assertRequestError(t, response, tt.wantStatus, tt.wantErrorCode, tt.wantErrorField)
					return
				}

				if response.Code != tt.wantStatus {
					t.Errorf("Status = %d; want %d", response.Code, tt.wantStatus)
				}
			})
		}
	})
	t.Run("usage batch media type", func(t *testing.T) {
		tests := []struct {
			name          string
			mediaType     string
			encoding      string
			wantStatus    int
			wantErrorCode string
		}{
			{
				name:          "missing media type",
				mediaType:     "",
				wantStatus:    415,
				wantErrorCode: "unsupported_media_type",
			},
			{
				name:          "plain text",
				mediaType:     "text/plain",
				wantStatus:    415,
				wantErrorCode: "unsupported_media_type",
			},
			{
				name:          "UTF-16 JSON",
				mediaType:     "application/json; charset=utf-16",
				wantStatus:    415,
				wantErrorCode: "unsupported_media_type",
			},
			{
				name:          "malformed media type",
				mediaType:     "application/json; broken",
				wantStatus:    415,
				wantErrorCode: "unsupported_media_type",
			},
			{
				name:       "UTF-8 JSON",
				mediaType:  "application/json; charset=UTF-8",
				wantStatus: 202,
			},
			{
				name:          "compressed JSON",
				mediaType:     "application/json",
				encoding:      "gzip",
				wantStatus:    415,
				wantErrorCode: "unsupported_media_type",
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				request := httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(validUsageBatch))
				request.Header.Set("Content-Type", tt.mediaType)
				request.Header.Set("Content-Encoding", tt.encoding)
				response := httptest.NewRecorder()
				httpapi.NewHandler(acceptingStore(), 10*time.Second, 32).ServeHTTP(response, request)
				if tt.wantErrorCode != "" {
					assertRequestError(t, response, tt.wantStatus, tt.wantErrorCode, "")
					return
				}

				if response.Code != tt.wantStatus {
					t.Errorf("Status = %d; want %d", response.Code, tt.wantStatus)
				}
			})
		}
	})
	t.Run("usage batch read failure", func(t *testing.T) {
		tt := struct {
			body          io.Reader
			wantStatus    int
			wantErrorCode string
		}{
			body:          failingReader{},
			wantStatus:    400,
			wantErrorCode: "invalid_json",
		}

		request := httptest.NewRequest(http.MethodPost, "/usage/batches", tt.body)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		httpapi.NewHandler(acceptingStore(), 10*time.Second, 32).ServeHTTP(response, request)
		assertRequestError(t, response, tt.wantStatus, tt.wantErrorCode, "")
	})
	t.Run("usage batch configured deadline", func(t *testing.T) {
		tt := struct {
			body             string
			wantStatus       int
			wantErrorCode    string
			ingestionTimeout time.Duration
			wantRetryAfter   string
		}{
			body:             validUsageBatch,
			wantStatus:       503,
			wantErrorCode:    "inbox_unavailable",
			ingestionTimeout: 20 * time.Millisecond,
			wantRetryAfter:   "1",
		}

		store := storeFunc(func(ctx context.Context, _ []usage.Event, _ time.Time) error {
			<-ctx.Done()
			return ctx.Err()
		})
		request := httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(tt.body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		httpapi.NewHandler(store, tt.ingestionTimeout, 32).ServeHTTP(response, request)
		assertRequestError(t, response, tt.wantStatus, tt.wantErrorCode, "")
		if response.Header().Get("Retry-After") != tt.wantRetryAfter {
			t.Error("Timed-out batch omitted retry guidance")
		}
	})
	t.Run("usage batch admission budget", func(t *testing.T) {
		tt := struct {
			maxInFlight           int
			body                  string
			wantRejectedStatus    int
			wantRejectedCode      string
			wantBodyRead          bool
			wantRetryAfter        string
			wantCallsWhileBlocked int32
			wantHealthStatus      int
			wantAcceptedStatus    int
			wantCallsAfterRelease int32
		}{
			maxInFlight:           1,
			body:                  validUsageBatch,
			wantRejectedStatus:    503,
			wantRejectedCode:      "inbox_unavailable",
			wantBodyRead:          false,
			wantRetryAfter:        "1",
			wantCallsWhileBlocked: 1,
			wantHealthStatus:      200,
			wantAcceptedStatus:    202,
			wantCallsAfterRelease: 2,
		}

		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		t.Cleanup(unblock)
		var calls atomic.Int32
		store := storeFunc(func(ctx context.Context, _ []usage.Event, _ time.Time) error {
			if calls.Add(1) == 1 {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}

			return nil
		})
		handler := httpapi.NewHandler(store, time.Second, tt.maxInFlight)
		first := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(tt.body))
		request.Header.Set("Content-Type", "application/json")
		finished := make(chan struct{})
		go func() { handler.ServeHTTP(first, request); close(finished) }()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("First batch did not reach storage")
		}

		rejected := httptest.NewRecorder()
		reader := &observedBody{}
		handler.ServeHTTP(rejected, httptest.NewRequest(http.MethodPost, "/usage/batches", reader))
		assertRequestError(t, rejected, tt.wantRejectedStatus, tt.wantRejectedCode, "")
		if reader.read != tt.wantBodyRead || rejected.Header().Get("Retry-After") != tt.wantRetryAfter || calls.Load() != tt.wantCallsWhileBlocked {
			t.Error("Rejected batch read its body, reached storage, or omitted retry guidance")
		}

		health := httptest.NewRecorder()
		handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if health.Code != tt.wantHealthStatus {
			t.Errorf("Health during overload = %d, want 200", health.Code)
		}

		unblock()
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("Admitted batch did not complete")
		}

		if first.Code != tt.wantAcceptedStatus {
			t.Errorf("Admitted batch = %d, want 202", first.Code)
		}

		response := httptest.NewRecorder()
		request = httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(tt.body))
		request.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(response, request)
		if response.Code != tt.wantAcceptedStatus || calls.Load() != tt.wantCallsAfterRelease {
			t.Error("Completed batch did not release its admission slot")
		}
	})
	t.Run("usage batch store input", func(t *testing.T) {
		tt := struct {
			body                string
			wantStatus          int
			wantCalls           int
			wantEvent           usage.Event
			wantMaximumDeadline time.Duration
		}{
			body: changeEvent(t, func(e map[string]any) {
				e["source"] = "  platform-test  "
				e["period_start"] = "2026-10-10T20:00:00+08:00"
				e["units"] = 0
			}),
			wantStatus:          202,
			wantCalls:           1,
			wantEvent:           usage.Event{Source: "  platform-test  ", EventID: "test-event", CustomerID: "acme", SandboxID: "sandbox-001", SchemaVersion: 1, Metric: "cpu_seconds", Units: 0, PeriodStart: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC), PeriodEnd: time.Date(2026, 10, 10, 13, 0, 0, 0, time.UTC)},
			wantMaximumDeadline: 10 * time.Second,
		}

		body := tt.body
		before := time.Now().UTC().Truncate(time.Microsecond)
		calls := 0
		store := storeFunc(func(ctx context.Context, events []usage.Event, receipt time.Time) error {
			calls++
			if len(events) != 1 {
				t.Fatalf("Stored batch length = %d, want 1", len(events))
			}

			event := events[0]
			if event != tt.wantEvent {
				t.Errorf("Parsed store input = %+v", event)
			}

			if event.PeriodStart.Location() != time.UTC || receipt.Location() != time.UTC ||
				receipt.Before(before) || receipt.After(time.Now()) || receipt.Nanosecond()%1_000 != 0 {
				t.Errorf("Receipt or consumption time was not supplied canonically: %s", receipt)
			}

			deadline, bounded := ctx.Deadline()
			if !bounded || time.Until(deadline) <= 0 || time.Until(deadline) > tt.wantMaximumDeadline {
				t.Errorf("Store context deadline = %s, bounded %v", deadline, bounded)
			}

			return nil
		})
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		httpapi.NewHandler(store, 10*time.Second, 32).ServeHTTP(response, request)
		if response.Code != tt.wantStatus || calls != tt.wantCalls {
			t.Errorf("Acceptance status = %d, store calls %d; want 202 and one call", response.Code, calls)
		}
	})
	t.Run("usage batch validation before storage", func(t *testing.T) {
		tt := struct {
			body           string
			wantStatus     int
			wantErrorCode  string
			wantErrorField string
			wantCalls      int
		}{
			body: changeBatch(t, func(batch map[string]any) {
				first := batch["events"].([]any)[0]
				second := map[string]any{}
				for key, value := range first.(map[string]any) {
					second[key] = value
				}

				second["event_id"], second["units"] = "second", -1
				batch["events"] = []any{first, second}
			}),
			wantStatus:     422,
			wantErrorCode:  "invalid_batch",
			wantErrorField: "events[1].units",
			wantCalls:      0,
		}

		body := tt.body
		calls := 0
		store := storeFunc(func(context.Context, []usage.Event, time.Time) error {
			calls++
			return nil
		})
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		httpapi.NewHandler(store, 10*time.Second, 32).ServeHTTP(response, request)
		assertRequestError(t, response, tt.wantStatus, tt.wantErrorCode, tt.wantErrorField)
		if calls != tt.wantCalls {
			t.Errorf("Invalid batch invoked storage %d times", calls)
		}
	})
	t.Run("usage batch waits for commit", func(t *testing.T) {
		tt := struct {
			body                  string
			requestTimeout        time.Duration
			wantStatusAfterCommit int
		}{
			body:                  validUsageBatch,
			requestTimeout:        2 * time.Second,
			wantStatusAfterCommit: 202,
		}

		entered, committed := make(chan struct{}), make(chan struct{})
		ctx, cancel := context.WithTimeout(context.Background(), tt.requestTimeout)
		defer cancel()
		store := storeFunc(func(ctx context.Context, _ []usage.Event, _ time.Time) error {
			close(entered)
			select {
			case <-committed:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		response := &observedResponse{ResponseRecorder: httptest.NewRecorder(), statuses: make(chan int, 1)}
		request := httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(tt.body)).WithContext(ctx)
		request.Header.Set("Content-Type", "application/json")
		done := make(chan struct{})
		go func() {
			httpapi.NewHandler(store, 10*time.Second, 32).ServeHTTP(response, request)
			close(done)
		}()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("Handler never reached storage")
		}

		select {
		case status := <-response.statuses:
			t.Fatalf("Response %d was sent before commit", status)
		default:
		}

		close(committed)
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("Handler did not finish after commit")
		}

		if response.Code != tt.wantStatusAfterCommit {
			t.Errorf("Committed response = %d, want 202", response.Code)
		}
	})
	t.Run("usage batch store failures", func(t *testing.T) {
		for _, tt := range []struct {
			name           string
			err            error
			wantStatus     int
			wantErrorCode  string
			wantSource     string
			wantEventID    string
			wantRetryAfter string
		}{
			{
				name:           "content conflict",
				err:            fmt.Errorf("wrapped: %w", &inbox.ConflictError{Source: "platform-test", EventID: "test-event"}),
				wantStatus:     409,
				wantErrorCode:  "event_conflict",
				wantSource:     "platform-test",
				wantEventID:    "test-event",
				wantRetryAfter: "",
			},
			{
				name:           "database unavailable",
				err:            errors.New("secret SQL connection details"),
				wantStatus:     503,
				wantErrorCode:  "inbox_unavailable",
				wantSource:     "",
				wantEventID:    "",
				wantRetryAfter: "1",
			},
			{
				name:           "unknown commit outcome",
				err:            errors.New("commit connection closed"),
				wantStatus:     503,
				wantErrorCode:  "inbox_unavailable",
				wantSource:     "",
				wantEventID:    "",
				wantRetryAfter: "1",
			},
			{
				name:           "canceled transaction",
				err:            context.Canceled,
				wantStatus:     503,
				wantErrorCode:  "inbox_unavailable",
				wantSource:     "",
				wantEventID:    "",
				wantRetryAfter: "1",
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				response := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(validUsageBatch))
				request.Header.Set("Content-Type", "application/json")
				store := storeFunc(func(context.Context, []usage.Event, time.Time) error { return tt.err })
				httpapi.NewHandler(store, 10*time.Second, 32).ServeHTTP(response, request)
				if strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Body.String(), "accepted") {
					t.Errorf("Failure response leaked details or acknowledged: %s", response.Body)
				}

				var body struct {
					Error struct {
						Source  string `json:"source"`
						EventID string `json:"event_id"`
					} `json:"error"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatalf("Decode storage failure: %v", err)
				}

				if tt.wantStatus == 409 && (body.Error.Source != tt.wantSource || body.Error.EventID != tt.wantEventID) {
					t.Errorf("Conflict identity = %+v", body.Error)
				}

				if tt.wantStatus == 503 && response.Header().Get("Retry-After") != tt.wantRetryAfter {
					t.Errorf("Retry-After = %q, want 1", response.Header().Get("Retry-After"))
				}

				assertRequestError(t, response, tt.wantStatus, tt.wantErrorCode, "")
			})
		}
	})
	t.Run("usage batch missing store", func(t *testing.T) {
		tt := struct {
			body                string
			wantStatus          int
			wantErrorCode       string
			wantHealthStatus    int
			wantHealthBodyBytes int
		}{
			body:                validUsageBatch,
			wantStatus:          503,
			wantErrorCode:       "inbox_unavailable",
			wantHealthStatus:    200,
			wantHealthBodyBytes: 0,
		}

		handler := httpapi.NewHandler(nil, 10*time.Second, 32)
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(tt.body))
		request.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(response, request)
		assertRequestError(t, response, tt.wantStatus, tt.wantErrorCode, "")
		health := httptest.NewRecorder()
		handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if health.Code != tt.wantHealthStatus || health.Body.Len() != tt.wantHealthBodyBytes {
			t.Errorf("Health = %d %s, want empty 200", health.Code, health.Body)
		}
	})
	t.Run("usage batch request deadline", func(t *testing.T) {
		tt := struct {
			body           string
			wantStatus     int
			wantErrorCode  string
			requestTimeout time.Duration
		}{
			body:           validUsageBatch,
			wantStatus:     503,
			wantErrorCode:  "inbox_unavailable",
			requestTimeout: 20 * time.Millisecond,
		}

		ctx, cancel := context.WithTimeout(context.Background(), tt.requestTimeout)
		defer cancel()
		store := storeFunc(func(ctx context.Context, _ []usage.Event, _ time.Time) error {
			<-ctx.Done()
			return ctx.Err()
		})
		request := httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(tt.body)).WithContext(ctx)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		httpapi.NewHandler(store, 10*time.Second, 32).ServeHTTP(response, request)
		assertRequestError(t, response, tt.wantStatus, tt.wantErrorCode, "")
	})
}

const validUsageBatch = `{"batch_id":"test","events":[{"source":"platform-test","event_id":"test-event","schema_version":1,"customer_id":"acme","sandbox_id":"sandbox-001","metric":"cpu_seconds","period_start":"2026-10-10T12:00:00Z","period_end":"2026-10-10T13:00:00Z","units":100000000}]}`

type failingReader struct{}

// Read supplies a deterministic transport failure to the request-body test.
func (failingReader) Read(_ []byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

// postBatch exercises the public handler with an unknown-length body, ensuring
// size enforcement reads the actual bytes rather than trusting Content-Length.
func postBatch(body, mediaType string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/usage/batches", io.NopCloser(strings.NewReader(body)))
	request.Header.Set("Content-Type", mediaType)
	response := httptest.NewRecorder()
	httpapi.NewHandler(acceptingStore(), 10*time.Second, 32).ServeHTTP(response, request)

	return response
}

// changeBatch makes a fresh copy of the valid fixture and changes the envelope,
// so each transport rejection case isolates its intended malformed input.
func changeBatch(t *testing.T, change func(map[string]any)) string {
	t.Helper()
	var batch map[string]any
	if err := json.Unmarshal([]byte(validUsageBatch), &batch); err != nil {
		t.Fatalf("Decode fixture: %v", err)
	}

	change(batch)
	body, err := json.Marshal(batch)
	if err != nil {
		t.Fatalf("Encode fixture: %v", err)
	}

	return string(body)
}

// changeEvent changes just the first measurement in a fresh envelope, preserving
// valid surrounding fields so the resulting error can be attributed correctly.
func changeEvent(t *testing.T, change func(map[string]any)) string {
	t.Helper()

	return changeBatch(t, func(batch map[string]any) {
		change(batch["events"].([]any)[0].(map[string]any))
	})
}

// assertRequestError checks the status and stable JSON diagnostics used by
// producers to distinguish transport errors from invalid measurement values.
func assertRequestError(t *testing.T, response *httptest.ResponseRecorder, status int, code, field string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("Status = %d, want %d; response: %s", response.Code, status, response.Body)
	}

	if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Error Content-Type = %q, want application/json", contentType)
	}

	var body struct {
		Error struct{ Code, Message, Field string } `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("Decode error response: %v", err)
	}

	if body.Error.Code != code || body.Error.Field != field || body.Error.Message == "" {
		t.Errorf("Error = %+v, want code %q, field %q, and a description", body.Error, code, field)
	}
}

// repeatedEvents builds an envelope with the explicit event count requested by
// a boundary case; it leaves the expected HTTP outcome in that case's table.
func repeatedEvents(t *testing.T, count int) string {
	t.Helper()

	return changeBatch(t, func(batch map[string]any) {
		event := batch["events"].([]any)[0]
		events := make([]any, count)
		for index := range events {
			events[index] = event
		}

		batch["events"] = events
	})
}

type storeFunc func(context.Context, []usage.Event, time.Time) error

// InsertBatch adapts an individual test scenario to the production store
// contract, allowing tests to control commit completion and failures.
func (store storeFunc) InsertBatch(ctx context.Context, events []usage.Event, receipt time.Time) error {
	return store(ctx, events, receipt)
}

// acceptingStore supplies successful storage for transport-only unit tests.
// Durable database behavior is exercised separately against PostgreSQL.
func acceptingStore() storeFunc {
	return func(context.Context, []usage.Event, time.Time) error { return nil }
}

type observedBody struct{ read bool }

// Read detects any attempt to allocate or decode an overloaded batch's body.
// The admission test expects this reader to remain untouched.
func (body *observedBody) Read([]byte) (int, error) {
	body.read = true
	return 0, errors.New("unexpected body read")
}

type observedResponse struct {
	*httptest.ResponseRecorder
	statuses chan int
}

// WriteHeader exposes response timing through a channel so the commit-order
// test can observe headers without racing the recorder's mutable state.
func (response *observedResponse) WriteHeader(status int) {
	response.statuses <- status
	response.ResponseRecorder.WriteHeader(status)
}
