package billing

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"e2b/billing-api/internal/accounting"
)

type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

func ValidateIdentifier(field, value string) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || strings.ContainsRune(value, 0) || len(value) > 256 {
		return &ValidationError{Field: field, Message: "Use nonblank Unicode without NUL, at most 256 UTF-8 bytes."}
	}
	return nil
}

func validateTime(field string, value time.Time) error {
	if _, err := accounting.UTCMonth(value); err != nil {
		return &ValidationError{Field: field, Message: err.Error()}
	}
	if value.Nanosecond()%1_000 != 0 {
		return &ValidationError{Field: field, Message: "Use at most microsecond precision."}
	}
	return nil
}

func validateCents(field string, value int64, positive bool) error {
	if value < 0 || (positive && value == 0) {
		return &ValidationError{Field: field, Message: fmt.Sprintf("Invalid cents: %d.", value)}
	}
	return nil
}
