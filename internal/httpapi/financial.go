package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"e2b/billing-api/internal/billing"
)

// commandJSON preserves the usage transport's exact-field and Unicode rules.
// Every declared command field is explicit; only named nullable fields allow null.
func commandJSON(w http.ResponseWriter, r *http.Request, target any, fields []string, nullable ...string) bool {
	media, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || (parameters["charset"] != "" && !strings.EqualFold(parameters["charset"], "utf-8")) || (r.Header.Get("Content-Encoding") != "" && !strings.EqualFold(r.Header.Get("Content-Encoding"), "identity")) {
		writeRequestError(w, &requestError{status: 415, Code: "unsupported_media_type", Message: "Use uncompressed application/json with UTF-8 encoding."})
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 65_536))
	if err != nil {
		var size *http.MaxBytesError
		status := 400
		if errors.As(err, &size) {
			status = 413
		}
		writeRequestError(w, &requestError{status: status, Code: "invalid_json", Message: "Cannot read command body; the maximum is 65536 bytes."})
		return false
	}
	if !utf8.Valid(body) || !json.Valid(body) || hasUnpairedSurrogate(body) {
		writeRequestError(w, invalidJSON("Use one valid Unicode JSON document.", ""))
		return false
	}
	values, err := decodeObject(body, fields)
	if err != nil {
		writeRequestError(w, invalidJSON(err.Error(), ""))
		return false
	}
	for _, field := range fields {
		raw, present := values[field]
		if !present || (isNull(raw) && !slices.Contains(nullable, field)) {
			writeRequestError(w, &requestError{status: 422, Code: "invalid_command", Message: "The field must be explicitly supplied.", Field: field})
			return false
		}
	}
	if err := json.Unmarshal(body, target); err != nil {
		writeRequestError(w, invalidJSON("Command field types or integer ranges are invalid.", ""))
		return false
	}
	return true
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
