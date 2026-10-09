package usage_test

import (
	"errors"
	"testing"
	"time"

	"e2b/billing-api/internal/usage"
)

// validEvent provides the assignment's first Acme measurement as a valid
// baseline, allowing each scenario to change only the value under test.
func validEvent() usage.Event {
	return usage.Event{
		Source:        "platform-simulator",
		EventID:       "acme-cpu-000001",
		SchemaVersion: 1,
		CustomerID:    "acme",
		SandboxID:     "acme-sandbox-001",
		Metric:        "cpu_seconds",
		PeriodStart:   time.Date(2_026, 10, 10, 12, 0, 0, 0, time.UTC),
		PeriodEnd:     time.Date(2_026, 10, 10, 13, 0, 0, 0, time.UTC),
		Units:         100_000_000,
	}
}

// TestEventValidateValid checks measurements that the standalone value contract
// must accept, including zero units, integer bounds, Unicode identifiers, and
// interval instants across offsets and UTC month boundaries. It also verifies
// that value validation does not assume customer, metric, or version registries.
func TestEventValidateValid(t *testing.T) {
	tests := []struct {
		name   string
		change func(*usage.Event)
	}{
		{"assignment measurement", func(_ *usage.Event) {}},
		{"zero units", func(e *usage.Event) { e.Units = 0 }},
		{"maximum units", func(e *usage.Event) { e.Units = 1<<63 - 1 }},
		{"maximum schema version", func(e *usage.Event) { e.SchemaVersion = 1<<31 - 1 }},
		{"no registry assumptions", func(e *usage.Event) {
			e.SchemaVersion = 2
			e.CustomerID = "another-customer"
			e.Metric = "another-metric"
		}},
		{"nonblank Unicode identifiers", func(e *usage.Event) {
			e.Source = "  zdroj-žluťoučký kůň  "
			e.EventID = "測定-001"
		}},
		{"UTC month boundary at microsecond precision", func(e *usage.Event) {
			e.PeriodStart = time.Date(2_026, 10, 31, 23, 59, 59, 999_999_000, time.UTC)
			e.PeriodEnd = time.Date(2_026, 11, 1, 0, 0, 0, 0, time.UTC)
		}},
		{"offset wall clock ends earlier but instant ends later", func(e *usage.Event) {
			e.PeriodStart = time.Date(2_026, 10, 10, 12, 0, 0, 0, time.FixedZone("UTC+02", 2*60*60))
			e.PeriodEnd = time.Date(2_026, 10, 10, 11, 0, 0, 0, time.UTC)
		}},
		{"Asia Shanghai across UTC month boundary", func(e *usage.Event) {
			zone := time.FixedZone("Asia/Shanghai", 8*60*60)
			e.PeriodStart = time.Date(2_026, 11, 1, 7, 30, 0, 0, zone)
			e.PeriodEnd = time.Date(2_026, 11, 1, 8, 0, 0, 0, zone)
		}},
		{"before Unix epoch", func(e *usage.Event) {
			e.PeriodStart = time.Date(1_969, 12, 31, 23, 59, 59, 999_999_000, time.UTC)
			e.PeriodEnd = time.Date(1_970, 1, 1, 0, 0, 0, 0, time.UTC)
		}},
		{"lowest UTC year", func(e *usage.Event) {
			e.PeriodStart = time.Date(1, 1, 1, 0, 0, 1, 0, time.UTC)
			e.PeriodEnd = e.PeriodStart.Add(time.Microsecond)
		}},
		{"highest UTC year", func(e *usage.Event) {
			e.PeriodStart = time.Date(9_999, 12, 31, 23, 59, 59, 999_998_000, time.UTC)
			e.PeriodEnd = e.PeriodStart.Add(time.Microsecond)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := validEvent()
			tt.change(&event)
			if err := event.Validate(); err != nil {
				t.Fatalf("Valid event rejected: %v", err)
			}
		})
	}
}

// TestEventValidateIdentifiers checks each identity, ownership, and metric field
// against empty, whitespace-only, and database-incompatible text. Every failure
// must identify the offending field so HTTP decoding can report it to producers.
func TestEventValidateIdentifiers(t *testing.T) {
	fields := []struct {
		name string
		set  func(*usage.Event, string)
	}{
		{"source", func(e *usage.Event, value string) { e.Source = value }},
		{"event_id", func(e *usage.Event, value string) { e.EventID = value }},
		{"customer_id", func(e *usage.Event, value string) { e.CustomerID = value }},
		{"sandbox_id", func(e *usage.Event, value string) { e.SandboxID = value }},
		{"metric", func(e *usage.Event, value string) { e.Metric = value }},
	}
	values := []struct {
		name  string
		value string
	}{
		{"missing", ""},
		{"spaces", "   "},
		{"tabs and newlines", "\t\r\n"},
		{"Unicode whitespace", " \t\u00a0\u2003\n"},
		{"NUL character", "producer\x00suffix"},
		{"invalid UTF-8", "producer\xff"},
	}
	for _, field := range fields {
		t.Run(field.name, func(t *testing.T) {
			for _, value := range values {
				t.Run(value.name, func(t *testing.T) {
					event := validEvent()
					field.set(&event, value.value)
					assertInvalidField(t, event, field.name)
				})
			}
		})
	}
}

// TestEventValidateInvalidNumbers rejects nonpositive schema versions and
// negative consumption, including the lowest int32 and int64 values, while
// returning the field-specific error required for actionable validation feedback.
func TestEventValidateInvalidNumbers(t *testing.T) {
	tests := []struct {
		name   string
		field  string
		change func(*usage.Event)
	}{
		{"missing schema version", "schema_version", func(e *usage.Event) { e.SchemaVersion = 0 }},
		{"negative schema version", "schema_version", func(e *usage.Event) { e.SchemaVersion = -1 }},
		{"minimum schema version", "schema_version", func(e *usage.Event) { e.SchemaVersion = -1 << 31 }},
		{"negative units", "units", func(e *usage.Event) { e.Units = -1 }},
		{"minimum units", "units", func(e *usage.Event) { e.Units = -1 << 63 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := validEvent()
			tt.change(&event)
			assertInvalidField(t, event, tt.field)
		})
	}
}

// TestEventValidateInvalidPeriods rejects absent, reversed, or equal interval
// endpoints and timestamps outside the UTC range or PostgreSQL precision.
// Offset scenarios verify that ordering uses instants rather than wall clocks,
// preventing invalid consumption intervals from reaching later storage.
func TestEventValidateInvalidPeriods(t *testing.T) {
	tests := []struct {
		name   string
		field  string
		change func(*usage.Event)
	}{
		{"missing start", "period_start", func(e *usage.Event) { e.PeriodStart = time.Time{} }},
		{"missing end", "period_end", func(e *usage.Event) { e.PeriodEnd = time.Time{} }},
		{"equal endpoints", "period_end", func(e *usage.Event) { e.PeriodEnd = e.PeriodStart }},
		{"end before start", "period_end", func(e *usage.Event) { e.PeriodEnd = e.PeriodStart.Add(-time.Microsecond) }},
		{"same instant with different offsets", "period_end", func(e *usage.Event) {
			e.PeriodEnd = e.PeriodStart.In(time.FixedZone("UTC+08", 8*60*60))
		}},
		{"offset wall clock ends later but instant ends earlier", "period_end", func(e *usage.Event) {
			e.PeriodEnd = time.Date(2_026, 10, 10, 13, 0, 0, 0, time.FixedZone("UTC+02", 2*60*60))
		}},
		{"submicrosecond start", "period_start", func(e *usage.Event) { e.PeriodStart = e.PeriodStart.Add(time.Nanosecond) }},
		{"submicrosecond end", "period_end", func(e *usage.Event) { e.PeriodEnd = e.PeriodEnd.Add(time.Nanosecond) }},
		{"UTC start year zero", "period_start", func(e *usage.Event) {
			e.PeriodStart = time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)
		}},
		{"UTC end year ten thousand", "period_end", func(e *usage.Event) {
			e.PeriodEnd = time.Date(10_000, 1, 1, 0, 0, 0, 0, time.UTC)
		}},
		{"offset moves start outside lowest UTC year", "period_start", func(e *usage.Event) {
			e.PeriodStart = time.Date(1, 1, 1, 0, 0, 0, 0, time.FixedZone("UTC+01", 60*60))
		}},
		{"offset moves end outside highest UTC year", "period_end", func(e *usage.Event) {
			e.PeriodEnd = time.Date(9_999, 12, 31, 23, 30, 0, 0, time.FixedZone("UTC-01", -60*60))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := validEvent()
			tt.change(&event)
			assertInvalidField(t, event, tt.field)
		})
	}
}

// assertInvalidField requires a structured ValidationError for the expected
// field, so rejection scenarios verify useful diagnostics as well as failure.
func assertInvalidField(t *testing.T, event usage.Event, field string) {
	t.Helper()
	err := event.Validate()
	var validationErr *usage.ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("Validate() error = %v, want a ValidationError for %s", err, field)
	}
	if validationErr.Field != field {
		t.Errorf("Invalid field = %q, want %q", validationErr.Field, field)
	}
}
