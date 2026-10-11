package billing

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"e2b/billing-api/internal/accounting"
)

const maxIdentifierBytes = 256

type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

func ValidateIdentifier(field, value string) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || strings.ContainsRune(value, 0) || len(value) > maxIdentifierBytes {
		return &ValidationError{Field: field, Message: "Use nonblank Unicode without NUL, at most 256 UTF-8 bytes."}
	}

	return nil
}

// ValidateTime enforces supported UTC years and PostgreSQL timestamp precision.
func ValidateTime(field string, value time.Time) error {
	if _, err := accounting.UTCMonth(value); err != nil {
		return &ValidationError{Field: field, Message: err.Error()}
	}

	if value.Nanosecond()%int(time.Microsecond) != 0 {
		return &ValidationError{Field: field, Message: "Use at most microsecond precision."}
	}

	return nil
}

// ValidateDate keeps application calendar values at midnight UTC in supported years.
func ValidateDate(field string, value time.Time) error {
	if err := ValidateTime(field, value); err != nil {
		return err
	}

	if !value.Equal(UTCDay(value)) {
		return &ValidationError{Field: field, Message: "Use the start of a UTC calendar day."}
	}

	return nil
}

// UTCDay resolves the beginning of the UTC day containing an instant.
func UTCDay(value time.Time) time.Time {
	utc := value.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
}

// ValidateCents accepts nonnegative prices and limits, and positive credit grants.
func ValidateCents(field string, value int64, positive bool) error {
	if value < 0 || (positive && value == 0) {
		return &ValidationError{Field: field, Message: fmt.Sprintf("Invalid cents: %d.", value)}
	}

	return nil
}
