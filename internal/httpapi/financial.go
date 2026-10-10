package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"

	"e2b/billing-api/internal/billing"
)

// commandJSON preserves the usage transport's exact-field and Unicode rules.
// Every declared command field is explicit; only named nullable fields allow null.
func commandJSON(w http.ResponseWriter, r *http.Request, target any, fields []string, nullable ...string) bool {
	body, err := readCommandBody(w, r)
	if err != nil {
		writeRequestError(w, err)
		return false
	}

	if err := parseCommandJSON(body, target, fields, nullable); err != nil {
		writeRequestError(w, err)
		return false
	}

	return true
}

func readCommandBody(w http.ResponseWriter, r *http.Request) ([]byte, *requestError) {
	if !usesJSONUTF8(r) || !usesIdentityEncoding(r) {
		return nil, &requestError{status: 415, Code: "unsupported_media_type", Message: "Use uncompressed application/json with UTF-8 encoding."}
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 65_536))
	if err != nil {
		var size *http.MaxBytesError
		status := 400
		if errors.As(err, &size) {
			status = 413
		}

		return nil, &requestError{status: status, Code: "invalid_json", Message: "Cannot read command body; the maximum is 65536 bytes."}
	}

	return body, nil
}

func parseCommandJSON(body []byte, target any, fields, nullable []string) *requestError {
	if !validUnicodeJSON(body) {
		return invalidJSON("Use one valid Unicode JSON document.", "")
	}

	values, err := decodeObject(body, fields)
	if err != nil {
		return invalidJSON(err.Error(), "")
	}

	for _, field := range fields {
		raw, present := values[field]
		if !present || (isNull(raw) && !slices.Contains(nullable, field)) {
			return &requestError{status: 422, Code: "invalid_command", Message: "The field must be explicitly supplied.", Field: field}
		}
	}

	if err := json.Unmarshal(body, target); err != nil {
		return invalidJSON("Command field types or integer ranges are invalid.", "")
	}

	return nil
}

func commandTime(value, field string) (time.Time, error) {
	parsed, err := parseTimestamp(value)
	if err != nil {
		return time.Time{}, &billing.ValidationError{Field: field, Message: err.Error()}
	}

	return parsed, nil
}

func financialResult(w http.ResponseWriter, value any, err error) {
	if err != nil {
		writeFinancialError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeFinancialError(w http.ResponseWriter, err error) {
	var validation *billing.ValidationError
	switch {
	case errors.As(err, &validation):
		writeRequestError(w, &requestError{status: 422, Code: "invalid_command", Message: validation.Message, Field: validation.Field})
	case errors.Is(err, billing.ErrNotFound):
		writeRequestError(w, &requestError{status: 404, Code: "not_found", Message: "The billing resource does not exist."})
	case errors.Is(err, billing.ErrConflict):
		writeRequestError(w, &requestError{status: 409, Code: "billing_conflict", Message: "The operation conflicts with financial history. Retain its identity and investigate."})
	default:
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, &requestError{status: 503, Code: "billing_unavailable", Message: "Billing storage is unavailable. Retry the same operation identity and content."})
	}
}

// financialContext gives every command and query a bounded database budget.
func financialContext(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), timeout)
}
