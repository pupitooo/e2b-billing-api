// Package usage defines the measurement contract supplied by the platform.
package usage

import (
	"strings"
	"time"
	"unicode/utf8"
)

// Event is a measured increment over the half-open consumption interval
// [PeriodStart, PeriodEnd). Source and EventID identify the measurement.
// Billing supplies receipt and processing metadata separately.
type Event struct {
	Source        string
	EventID       string
	SchemaVersion int32
	CustomerID    string
	SandboxID     string
	Metric        string
	PeriodStart   time.Time
	PeriodEnd     time.Time
	Units         int64
}

// ValidationError identifies the first field that violates the event contract.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return e.Field + ": " + e.Message
}

// Validate checks measurement values without changing the supplied content.
// Customer existence, supported versions, and supported metrics require their
// own registries. Zero units are valid; HTTP decoding must check field presence
// separately so an omitted units field cannot become a zero measurement.
func (e Event) Validate() error {
	for _, identifier := range []struct {
		field string
		value string
	}{
		{"source", e.Source},
		{"event_id", e.EventID},
		{"customer_id", e.CustomerID},
		{"sandbox_id", e.SandboxID},
		{"metric", e.Metric},
	} {
		if !utf8.ValidString(identifier.value) {
			return &ValidationError{identifier.field, "must be valid UTF-8"}
		}
		if strings.ContainsRune(identifier.value, '\x00') {
			return &ValidationError{identifier.field, "must not contain a NUL character"}
		}
		if strings.TrimSpace(identifier.value) == "" {
			return &ValidationError{identifier.field, "must contain a non-whitespace character"}
		}
	}

	if e.SchemaVersion <= 0 {
		return &ValidationError{"schema_version", "must be positive"}
	}
	if e.Units < 0 {
		return &ValidationError{"units", "must be non-negative"}
	}
	if err := validateTimestamp("period_start", e.PeriodStart); err != nil {
		return err
	}
	if err := validateTimestamp("period_end", e.PeriodEnd); err != nil {
		return err
	}
	if !e.PeriodEnd.After(e.PeriodStart) {
		return &ValidationError{"period_end", "must be later than period_start"}
	}
	return nil
}

func validateTimestamp(field string, value time.Time) error {
	if value.IsZero() {
		return &ValidationError{field, "is required"}
	}
	// Keep UTC instants representable by the RFC 3339 transport contract.
	if year := value.UTC().Year(); year < 1 || year > 9_999 {
		return &ValidationError{field, "must have a UTC year between 1 and 9999"}
	}
	// PostgreSQL stores microseconds. Reject finer values so a later retry can
	// compare the original measurement without losing timestamp precision.
	if value.Nanosecond()%int(time.Microsecond) != 0 {
		return &ValidationError{field, "must use at most microsecond precision"}
	}
	return nil
}
