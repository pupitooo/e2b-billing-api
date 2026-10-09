package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"e2b/billing-api/internal/httpapi"
)

const validUsageBatch = `{"batch_id":"test","events":[{"source":"platform-test","event_id":"test-event","schema_version":1,"customer_id":"acme","sandbox_id":"sandbox-001","metric":"cpu_seconds","period_start":"2026-10-10T12:00:00Z","period_end":"2026-10-10T13:00:00Z","units":100000000}]}`

// TestUsageBatchValidInput verifies accepted transport forms, explicit zero
// units, integer bounds, Unicode, and equivalent UTC-offset representations.
// Valid requests reach a store double that successfully confirms the batch.
func TestUsageBatchValidInput(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"assignment input", validUsageBatch},
		{"optional batch identifier", changeBatch(t, func(b map[string]any) { delete(b, "batch_id") })},
		{"zero units", changeEvent(t, func(e map[string]any) { e["units"] = 0 })},
		{"maximum units", changeEvent(t, func(e map[string]any) { e["units"] = json.Number("9223372036854775807") })},
		{"maximum version", changeEvent(t, func(e map[string]any) { e["schema_version"] = json.Number("2147483647") })},
		{"Unicode byte boundary", changeEvent(t, func(e map[string]any) { e["source"] = strings.Repeat("ž", 128) })},
		{"UTC month boundary", changeEvent(t, func(e map[string]any) {
			e["period_start"] = "2026-10-31T23:59:59.999999Z"
			e["period_end"] = "2026-11-01T08:00:00+08:00"
		})},
		{"offset wall clock comparison", changeEvent(t, func(e map[string]any) {
			e["period_start"] = "2026-10-10T12:00:00+02:00"
			e["period_end"] = "2026-10-10T11:00:00Z"
		})},
		{"valid surrogate pair", strings.Replace(validUsageBatch, "test-event", `\ud83d\ude80`, 1)},
		{"escaped literal surrogate text", strings.Replace(validUsageBatch, "test-event", `\\ud800`, 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := postBatch(tt.body, "application/json")
			if response.Code != http.StatusAccepted {
				t.Fatalf("Status = %d, want 202; response: %s", response.Code, response.Body)
			}
		})
	}
}

// TestUsageBatchRequiredFields checks every required event member both absent
// and explicitly null. In particular, omitted units must fail even though an
// explicitly supplied zero measurement is valid.
func TestUsageBatchRequiredFields(t *testing.T) {
	for _, field := range []string{"source", "event_id", "schema_version", "customer_id", "sandbox_id", "metric", "period_start", "period_end", "units"} {
		t.Run(field, func(t *testing.T) {
			for _, missing := range []bool{true, false} {
				name := "null"
				if missing {
					name = "missing"
				}
				t.Run(name, func(t *testing.T) {
					body := changeEvent(t, func(e map[string]any) {
						if missing {
							delete(e, field)
						} else {
							e[field] = nil
						}
					})
					assertRequestError(t, postBatch(body, "application/json"), 422, "invalid_batch", "events[0]."+field)
				})
			}
		})
	}
}

// TestUsageBatchInvalidJSON rejects syntax errors, incorrect JSON types,
// unknown/case-aliased/duplicate members, and malformed Unicode before their
// values can be normalized into an ambiguous identity or measurement.
func TestUsageBatchInvalidJSON(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		field string
	}{
		{"empty body", "", ""},
		{"truncated document", `{"events":[`, ""},
		{"multiple documents", validUsageBatch + `{}`, ""},
		{"top-level array", `[]`, ""},
		{"top-level null", `null`, ""},
		{"unknown envelope member", changeBatch(t, func(b map[string]any) { b["extra"] = true }), ""},
		{"duplicate envelope member", strings.Replace(validUsageBatch, `"batch_id":"test"`, `"batch_id":"test","batch_id":"other"`, 1), ""},
		{"case-aliased envelope member", strings.Replace(validUsageBatch, `"events"`, `"Events"`, 1), ""},
		{"object instead of array", `{"events":{}}`, "events"},
		{"null event", `{"events":[null]}`, "events[0]"},
		{"unknown event member", changeEvent(t, func(e map[string]any) { e["received_at"] = "2026-10-10T13:00:00Z" }), "events[0]"},
		{"duplicate event member", strings.Replace(validUsageBatch, `"event_id":"test-event"`, `"event_id":"test-event","event_id":"other"`, 1), "events[0]"},
		{"case-aliased event member", strings.Replace(validUsageBatch, `"event_id"`, `"EVENT_ID"`, 1), "events[0]"},
		{"numeric identifier", changeEvent(t, func(e map[string]any) { e["source"] = 1 }), "events[0]"},
		{"fractional units", changeEvent(t, func(e map[string]any) { e["units"] = 1.5 }), "events[0]"},
		{"exponent units", changeEvent(t, func(e map[string]any) { e["units"] = json.Number("1e3") }), "events[0]"},
		{"units overflow", changeEvent(t, func(e map[string]any) { e["units"] = json.Number("9223372036854775808") }), "events[0]"},
		{"version overflow", changeEvent(t, func(e map[string]any) { e["schema_version"] = json.Number("2147483648") }), "events[0]"},
		{"invalid UTF-8", strings.Replace(validUsageBatch, "test-event", "\xff", 1), ""},
		{"unpaired high surrogate", strings.Replace(validUsageBatch, "test-event", `\ud800`, 1), ""},
		{"unpaired low surrogate", strings.Replace(validUsageBatch, "test-event", `\udfff`, 1), ""},
		{"high surrogate followed by ordinary escape", strings.Replace(validUsageBatch, "test-event", `\ud800\u0041`, 1), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertRequestError(t, postBatch(tt.body, "application/json"), 400, "invalid_json", tt.field)
		})
	}
}

// TestUsageBatchInvalidValues checks semantic rejection of otherwise correctly
// typed JSON: required envelope values, identifiers, counts, and finite ordered
// intervals with the precision that PostgreSQL will preserve.
func TestUsageBatchInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		field string
	}{
		{"missing events", `{}`, "events"},
		{"null events", `{"events":null}`, "events"},
		{"empty events", `{"events":[]}`, "events"},
		{"null batch identifier", changeBatch(t, func(b map[string]any) { b["batch_id"] = nil }), "batch_id"},
		{"blank batch identifier", changeBatch(t, func(b map[string]any) { b["batch_id"] = "\t\n" }), "batch_id"},
		{"long batch identifier", changeBatch(t, func(b map[string]any) { b["batch_id"] = strings.Repeat("x", 257) }), "batch_id"},
		{"NUL batch identifier", changeBatch(t, func(b map[string]any) { b["batch_id"] = "a\x00b" }), "batch_id"},
		{"Unicode whitespace identifier", changeEvent(t, func(e map[string]any) { e["source"] = "\t\u00a0\u2003\n" }), "events[0].source"},
		{"identifier exceeds UTF-8 byte limit", changeEvent(t, func(e map[string]any) { e["source"] = strings.Repeat("ž", 129) }), "events[0].source"},
		{"NUL identifier", changeEvent(t, func(e map[string]any) { e["event_id"] = "a\x00b" }), "events[0].event_id"},
		{"zero schema version", changeEvent(t, func(e map[string]any) { e["schema_version"] = 0 }), "events[0].schema_version"},
		{"negative units", changeEvent(t, func(e map[string]any) { e["units"] = -1 }), "events[0].units"},
		{"infinite timestamp", changeEvent(t, func(e map[string]any) { e["period_end"] = "infinity" }), "events[0].period_end"},
		{"missing timestamp offset", changeEvent(t, func(e map[string]any) { e["period_start"] = "2026-10-10T12:00:00" }), "events[0].period_start"},
		{"invalid offset hour", changeEvent(t, func(e map[string]any) { e["period_start"] = "2026-10-10T12:00:00+24:00" }), "events[0].period_start"},
		{"invalid offset minute", changeEvent(t, func(e map[string]any) { e["period_start"] = "2026-10-10T12:00:00+00:60" }), "events[0].period_start"},
		{"invalid calendar date", changeEvent(t, func(e map[string]any) { e["period_start"] = "2026-02-30T12:00:00Z" }), "events[0].period_start"},
		{"submicrosecond timestamp", changeEvent(t, func(e map[string]any) { e["period_start"] = "2026-10-10T12:00:00.0000001Z" }), "events[0].period_start"},
		{"equal instants in different offsets", changeEvent(t, func(e map[string]any) { e["period_end"] = "2026-10-10T20:00:00+08:00" }), "events[0].period_end"},
		{"end before start", changeEvent(t, func(e map[string]any) { e["period_end"] = "2026-10-10T11:59:59Z" }), "events[0].period_end"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertRequestError(t, postBatch(tt.body, "application/json"), 422, "invalid_batch", tt.field)
		})
	}
}

// TestUsageBatchLimits exercises both inclusive transport limits. Oversized
// bodies return 413 even with chunked-style unknown lengths; excess event counts
// return 422 without treating a valid prefix of the array as an accepted batch.
func TestUsageBatchLimits(t *testing.T) {
	const limit = 1 << 20
	atLimit := validUsageBatch + strings.Repeat(" ", limit-len(validUsageBatch))
	if response := postBatch(atLimit, "application/json"); response.Code != 202 {
		t.Fatalf("Body at byte limit: status = %d, want 202", response.Code)
	}
	assertRequestError(t, postBatch(atLimit+" ", "application/json"), 413, "request_too_large", "")
	for _, count := range []int{1000, 1001} {
		body := changeBatch(t, func(b map[string]any) {
			event := b["events"].([]any)[0]
			events := make([]any, count)
			for index := range events {
				events[index] = event
			}
			b["events"] = events
		})
		response := postBatch(body, "application/json")
		if count == 1000 {
			if response.Code != 202 {
				t.Fatalf("Event count at limit: status = %d, want 202", response.Code)
			}
		} else {
			assertRequestError(t, response, 422, "invalid_batch", "events")
		}
	}
}

// TestUsageBatchMediaType requires uncompressed UTF-8 JSON while accepting the
// standard optional charset parameter. Producers receive 415 for unsupported
// encodings rather than an acknowledgement of a body that was never decoded.
func TestUsageBatchMediaType(t *testing.T) {
	for _, mediaType := range []string{"", "text/plain", "application/json; charset=utf-16", "application/json; broken"} {
		assertRequestError(t, postBatch(validUsageBatch, mediaType), 415, "unsupported_media_type", "")
	}
	if response := postBatch(validUsageBatch, "application/json; charset=UTF-8"); response.Code != 202 {
		t.Fatalf("UTF-8 JSON status = %d, want 202", response.Code)
	}
	request := httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(validUsageBatch))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "gzip")
	response := httptest.NewRecorder()
	httpapi.NewHandler(acceptingStore(), 10*time.Second, 32).ServeHTTP(response, request)
	assertRequestError(t, response, 415, "unsupported_media_type", "")
}

// TestUsageBatchReadFailure simulates interrupted body transfer. A request whose
// body cannot be read must fail with a JSON error before reaching storage.
func TestUsageBatchReadFailure(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/usage/batches", failingReader{})
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpapi.NewHandler(acceptingStore(), 10*time.Second, 32).ServeHTTP(response, request)
	assertRequestError(t, response, 400, "invalid_json", "")
}

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
