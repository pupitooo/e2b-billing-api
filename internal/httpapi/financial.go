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

const maxCommandBytes = 65_536

// commandJSON preserves the usage transport's exact-field and Unicode rules.
// Every declared command field is explicit; only named nullable fields allow null.
func commandJSON(
	w http.ResponseWriter,
	r *http.Request,
	target any,
	requiredFields []string,
	nullableFields ...string,
) bool {
	body, err := readCommandBody(w, r)
	if err != nil {
		writeRequestError(w, err)
		return false
	}

	if err := parseCommandJSON(body, target, requiredFields, nullableFields); err != nil {
		writeRequestError(w, err)
		return false
	}

	return true
}

func readCommandBody(w http.ResponseWriter, r *http.Request) ([]byte, *requestError) {
	if !usesJSONUTF8(r) || !usesIdentityEncoding(r) {
		return nil, &requestError{
			status:  http.StatusUnsupportedMediaType,
			Code:    "unsupported_media_type",
			Message: "Use uncompressed application/json with UTF-8 encoding.",
		}
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCommandBytes))
	if err != nil {
		var size *http.MaxBytesError
		status := http.StatusBadRequest
		if errors.As(err, &size) {
			status = http.StatusRequestEntityTooLarge
		}

		return nil, &requestError{
			status:  status,
			Code:    "invalid_json",
			Message: "Cannot read command body; the maximum is 65536 bytes.",
		}
	}

	return body, nil
}

func parseCommandJSON(body []byte, target any, requiredFields, nullableFields []string) *requestError {
	if !validUnicodeJSON(body) {
		return invalidJSON("Use one valid Unicode JSON document.", "")
	}

	values, err := decodeObject(body, requiredFields)
	if err != nil {
		return invalidJSON(err.Error(), "")
	}

	for _, field := range requiredFields {
		raw, present := values[field]
		if !present || (isNull(raw) && !slices.Contains(nullableFields, field)) {
			return &requestError{
				status:  http.StatusUnprocessableEntity,
				Code:    "invalid_command",
				Message: "The field must be explicitly supplied.",
				Field:   field,
			}
		}
	}

	if err := json.Unmarshal(body, target); err != nil {
		return invalidJSON("Command field types or integer ranges are invalid.", "")
	}

	return nil
}

// commandDate accepts a calendar date only and resolves midnight in UTC.
func commandDate(value, field string) (time.Time, error) {
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil || parsed.Format(time.DateOnly) != value {
		return time.Time{}, &billing.ValidationError{Field: field, Message: "Use a valid calendar date in YYYY-MM-DD format."}
	}

	if err := billing.ValidateDate(field, parsed); err != nil {
		return time.Time{}, err
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
		writeRequestError(w, &requestError{
			status:  http.StatusUnprocessableEntity,
			Code:    "invalid_command",
			Message: validation.Message,
			Field:   validation.Field,
		})
	case errors.Is(err, billing.ErrNotFound):
		writeRequestError(w, &requestError{
			status:  http.StatusNotFound,
			Code:    "not_found",
			Message: "The billing resource does not exist.",
		})
	case errors.Is(err, billing.ErrConflict):
		writeRequestError(w, &requestError{
			status:  http.StatusConflict,
			Code:    "billing_conflict",
			Message: "The operation conflicts with financial history. Retain its identity and investigate.",
		})
	default:
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, &requestError{
			status:  http.StatusServiceUnavailable,
			Code:    "billing_unavailable",
			Message: "Billing storage is unavailable. Retry the same operation identity and content.",
		})
	}
}

// financialContext gives every command and query a bounded database budget.
func financialContext(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), timeout)
}
