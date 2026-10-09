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
		PeriodStart:   time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC),
		PeriodEnd:     time.Date(2026, 10, 10, 13, 0, 0, 0, time.UTC),
		Units:         100_000_000,
	}
}

// Event.Validate accepts the supported value contract and reports the exact
// offending field for invalid identifiers, numbers, precision, and UTC periods.
func TestEventValidate(t *testing.T) {
	tests := []struct {
		name           string
		change         func(*usage.Event)
		wantError      bool
		wantErrorField string
	}{
		{
			name:           "assignment measurement",
			change:         func(_ *usage.Event) {},
			wantError:      false,
			wantErrorField: "",
		},
		{
			name:           "zero units",
			change:         func(e *usage.Event) { e.Units = 0 },
			wantError:      false,
			wantErrorField: "",
		},
		{
			name:           "maximum units",
			change:         func(e *usage.Event) { e.Units = 1<<63 - 1 },
			wantError:      false,
			wantErrorField: "",
		},
		{
			name:           "maximum schema version",
			change:         func(e *usage.Event) { e.SchemaVersion = 1<<31 - 1 },
			wantError:      false,
			wantErrorField: "",
		},
		{
			name: "no registry assumptions",
			change: func(e *usage.Event) {
				e.SchemaVersion = 2
				e.CustomerID = "another-customer"
				e.Metric = "another-metric"
			},
			wantError:      false,
			wantErrorField: "",
		},
		{
			name: "nonblank Unicode identifiers",
			change: func(e *usage.Event) {
				e.Source = "  zdroj-žluťoučký kůň  "
				e.EventID = "測定-001"
			},
			wantError:      false,
			wantErrorField: "",
		},
		{
			name: "UTC month boundary at microsecond precision",
			change: func(e *usage.Event) {
				e.PeriodStart = time.Date(2026, 10, 31, 23, 59, 59, 999_999_000, time.UTC)
				e.PeriodEnd = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
			},
			wantError:      false,
			wantErrorField: "",
		},
		{
			name: "offset wall clock ends earlier but instant ends later",
			change: func(e *usage.Event) {
				e.PeriodStart = time.Date(2026, 10, 10, 12, 0, 0, 0, time.FixedZone("UTC+02", 2*60*60))
				e.PeriodEnd = time.Date(2026, 10, 10, 11, 0, 0, 0, time.UTC)
			},
			wantError:      false,
			wantErrorField: "",
		},
		{
			name: "Asia Shanghai across UTC month boundary",
			change: func(e *usage.Event) {
				zone := time.FixedZone("Asia/Shanghai", 8*60*60)
				e.PeriodStart = time.Date(2026, 11, 1, 7, 30, 0, 0, zone)
				e.PeriodEnd = time.Date(2026, 11, 1, 8, 0, 0, 0, zone)
			},
			wantError:      false,
			wantErrorField: "",
		},
		{
			name: "before Unix epoch",
			change: func(e *usage.Event) {
				e.PeriodStart = time.Date(1969, 12, 31, 23, 59, 59, 999_999_000, time.UTC)
				e.PeriodEnd = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
			},
			wantError:      false,
			wantErrorField: "",
		},
		{
			name: "lowest UTC year",
			change: func(e *usage.Event) {
				e.PeriodStart = time.Date(1000, 1, 1, 0, 0, 0, 0, time.UTC)
				e.PeriodEnd = e.PeriodStart.Add(time.Microsecond)
			},
			wantError:      false,
			wantErrorField: "",
		},
		{
			name: "local year below minimum becomes valid in UTC",
			change: func(e *usage.Event) {
				e.PeriodStart = time.Date(999, 12, 31, 23, 30, 0, 0, time.FixedZone("UTC-01", -60*60))
				e.PeriodEnd = e.PeriodStart.Add(time.Hour)
			},
			wantError:      false,
			wantErrorField: "",
		},
		{
			name: "highest UTC year",
			change: func(e *usage.Event) {
				e.PeriodStart = time.Date(9999, 12, 31, 23, 59, 59, 999_998_000, time.UTC)
				e.PeriodEnd = e.PeriodStart.Add(time.Microsecond)
			},
			wantError:      false,
			wantErrorField: "",
		},
		{
			name:           "missing schema version",
			change:         func(e *usage.Event) { e.SchemaVersion = 0 },
			wantError:      true,
			wantErrorField: "schema_version",
		},
		{
			name:           "negative schema version",
			change:         func(e *usage.Event) { e.SchemaVersion = -1 },
			wantError:      true,
			wantErrorField: "schema_version",
		},
		{
			name:           "minimum schema version",
			change:         func(e *usage.Event) { e.SchemaVersion = -1 << 31 },
			wantError:      true,
			wantErrorField: "schema_version",
		},
		{
			name:           "negative units",
			change:         func(e *usage.Event) { e.Units = -1 },
			wantError:      true,
			wantErrorField: "units",
		},
		{
			name:           "minimum units",
			change:         func(e *usage.Event) { e.Units = -1 << 63 },
			wantError:      true,
			wantErrorField: "units",
		},
		{
			name:           "missing start",
			change:         func(e *usage.Event) { e.PeriodStart = time.Time{} },
			wantError:      true,
			wantErrorField: "period_start",
		},
		{
			name:           "missing end",
			change:         func(e *usage.Event) { e.PeriodEnd = time.Time{} },
			wantError:      true,
			wantErrorField: "period_end",
		},
		{
			name:           "equal endpoints",
			change:         func(e *usage.Event) { e.PeriodEnd = e.PeriodStart },
			wantError:      true,
			wantErrorField: "period_end",
		},
		{
			name:           "end before start",
			change:         func(e *usage.Event) { e.PeriodEnd = e.PeriodStart.Add(-time.Microsecond) },
			wantError:      true,
			wantErrorField: "period_end",
		},
		{
			name: "same instant with different offsets",
			change: func(e *usage.Event) {
				e.PeriodEnd = e.PeriodStart.In(time.FixedZone("UTC+08", 8*60*60))
			},
			wantError:      true,
			wantErrorField: "period_end",
		},
		{
			name: "offset wall clock ends later but instant ends earlier",
			change: func(e *usage.Event) {
				e.PeriodEnd = time.Date(2026, 10, 10, 13, 0, 0, 0, time.FixedZone("UTC+02", 2*60*60))
			},
			wantError:      true,
			wantErrorField: "period_end",
		},
		{
			name:           "submicrosecond start",
			change:         func(e *usage.Event) { e.PeriodStart = e.PeriodStart.Add(time.Nanosecond) },
			wantError:      true,
			wantErrorField: "period_start",
		},
		{
			name:           "submicrosecond end",
			change:         func(e *usage.Event) { e.PeriodEnd = e.PeriodEnd.Add(time.Nanosecond) },
			wantError:      true,
			wantErrorField: "period_end",
		},
		{
			name: "UTC start year zero",
			change: func(e *usage.Event) {
				e.PeriodStart = time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)
			},
			wantError:      true,
			wantErrorField: "period_start",
		},
		{
			name: "UTC start before minimum year",
			change: func(e *usage.Event) {
				e.PeriodStart = time.Date(999, 12, 31, 23, 59, 59, 999_999_000, time.UTC)
			},
			wantError:      true,
			wantErrorField: "period_start",
		},
		{
			name: "UTC end before minimum year",
			change: func(e *usage.Event) {
				e.PeriodEnd = time.Date(999, 12, 31, 23, 59, 59, 999_999_000, time.UTC)
			},
			wantError:      true,
			wantErrorField: "period_end",
		},
		{
			name: "UTC end year ten thousand",
			change: func(e *usage.Event) {
				e.PeriodEnd = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			},
			wantError:      true,
			wantErrorField: "period_end",
		},
		{
			name: "offset moves start outside lowest UTC year",
			change: func(e *usage.Event) {
				e.PeriodStart = time.Date(1000, 1, 1, 0, 0, 0, 0, time.FixedZone("UTC+01", 60*60))
			},
			wantError:      true,
			wantErrorField: "period_start",
		},
		{
			name: "offset moves end outside highest UTC year",
			change: func(e *usage.Event) {
				e.PeriodEnd = time.Date(9999, 12, 31, 23, 30, 0, 0, time.FixedZone("UTC-01", -60*60))
			},
			wantError:      true,
			wantErrorField: "period_end",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := validEvent()
			tt.change(&input)

			err := input.Validate()
			if (err != nil) != tt.wantError {
				t.Fatalf("Validate(%+v) error = %v; want error=%t", input, err, tt.wantError)
			}
			if !tt.wantError {
				return
			}
			var validationError *usage.ValidationError
			if !errors.As(err, &validationError) {
				t.Fatalf("Validate error = %v; want ValidationError", err)
			}
			if validationError.Field != tt.wantErrorField {
				t.Errorf("Validate(%+v) error field = %q; want %q", input, validationError.Field, tt.wantErrorField)
			}
		})
	}
	t.Run("invalid identifiers", func(t *testing.T) {

		fields := []struct {
			name           string
			wantErrorField string
			set            func(*usage.Event, string)
		}{
			{
				name:           "source",
				wantErrorField: "source",
				set:            func(e *usage.Event, value string) { e.Source = value },
			},
			{
				name:           "event_id",
				wantErrorField: "event_id",
				set:            func(e *usage.Event, value string) { e.EventID = value },
			},
			{
				name:           "customer_id",
				wantErrorField: "customer_id",
				set:            func(e *usage.Event, value string) { e.CustomerID = value },
			},
			{
				name:           "sandbox_id",
				wantErrorField: "sandbox_id",
				set:            func(e *usage.Event, value string) { e.SandboxID = value },
			},
			{
				name:           "metric",
				wantErrorField: "metric",
				set:            func(e *usage.Event, value string) { e.Metric = value },
			},
		}
		values := []struct {
			name      string
			value     string
			wantError bool
		}{
			{
				name:      "missing",
				value:     "",
				wantError: true,
			},
			{
				name:      "spaces",
				value:     "   ",
				wantError: true,
			},
			{
				name:      "tabs and newlines",
				value:     "\t\r\n",
				wantError: true,
			},
			{
				name:      "Unicode whitespace",
				value:     " \t\u00a0\u2003\n",
				wantError: true,
			},
			{
				name:      "NUL character",
				value:     "producer\x00suffix",
				wantError: true,
			},
			{
				name:      "invalid UTF-8",
				value:     "producer\xff",
				wantError: true,
			},
		}
		for _, field := range fields {
			t.Run(field.name, func(t *testing.T) {
				for _, value := range values {
					t.Run(value.name, func(t *testing.T) {
						event := validEvent()
						field.set(&event, value.value)
						err := event.Validate()
						if (err != nil) != value.wantError {
							t.Fatalf("Validate(%+v) error = %v; want error=%t", event, err, value.wantError)
						}
						var validationError *usage.ValidationError
						if !errors.As(err, &validationError) {
							t.Fatalf("Validate error = %v; want ValidationError", err)
						}
						if validationError.Field != field.wantErrorField {
							t.Errorf("Error field = %q; want %q", validationError.Field, field.wantErrorField)
						}
					})
				}
			})
		}

	})
}
