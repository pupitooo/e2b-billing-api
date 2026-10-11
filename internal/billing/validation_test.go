package billing

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestValidateIdentifier verifies that identifiers are bounded by UTF-8 bytes,
// accepting the exact boundary and rejecting blank, NUL, malformed Unicode,
// and oversized identifiers with the original field and literal error message.
func TestValidateIdentifier(t *testing.T) {
	cases := []struct {
		name             string
		field            string
		value            string
		wantError        bool
		wantErrorField   string
		wantErrorMessage string
	}{
		{
			name: "empty identifier", field: "customer_id", value: "", wantError: true,
			wantErrorField: "customer_id", wantErrorMessage: "Use nonblank Unicode without NUL, at most 256 UTF-8 bytes.",
		},
		{
			name: "Unicode whitespace only", field: "customer_id", value: "\t\n\u00a0", wantError: true,
			wantErrorField: "customer_id", wantErrorMessage: "Use nonblank Unicode without NUL, at most 256 UTF-8 bytes.",
		},
		{
			name: "embedded NUL", field: "addon_name", value: "a\x00b", wantError: true,
			wantErrorField: "addon_name", wantErrorMessage: "Use nonblank Unicode without NUL, at most 256 UTF-8 bytes.",
		},
		{
			name: "invalid UTF-8", field: "metric", value: "\xff", wantError: true,
			wantErrorField: "metric", wantErrorMessage: "Use nonblank Unicode without NUL, at most 256 UTF-8 bytes.",
		},
		{
			name: "surrounding whitespace preserves a nonblank identifier", field: "customer_id", value: " acme ", wantError: false,
		},
		{
			name:      "ASCII at byte limit",
			field:     "price_version_id",
			value:     strings.Repeat("a", 256),
			wantError: false,
		},
		{
			name:             "ASCII above byte limit",
			field:            "price_version_id",
			value:            strings.Repeat("a", 257),
			wantError:        true,
			wantErrorField:   "price_version_id",
			wantErrorMessage: "Use nonblank Unicode without NUL, at most 256 UTF-8 bytes.",
		},
		{
			name:      "Unicode at byte limit",
			field:     "metric",
			value:     strings.Repeat("ž", 128),
			wantError: false,
		},
		{
			name:             "Unicode one byte above limit despite fewer than 256 characters",
			field:            "metric",
			value:            strings.Repeat("ž", 128) + "a",
			wantError:        true,
			wantErrorField:   "metric",
			wantErrorMessage: "Use nonblank Unicode without NUL, at most 256 UTF-8 bytes.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateIdentifier(tc.field, tc.value)

			if (err != nil) != tc.wantError {
				t.Fatalf("ValidateIdentifier(%q, %q): error=%v, wantError=%t", tc.field, tc.value, err, tc.wantError)
			}

			if !tc.wantError {
				return
			}

			var validationError *ValidationError
			if !errors.As(err, &validationError) {
				t.Fatalf("ValidateIdentifier(%q, %q): error=%T, want *ValidationError", tc.field, tc.value, err)
			}

			if validationError.Field != tc.wantErrorField {
				t.Errorf("ValidateIdentifier(%q, %q): error field=%q, want %q", tc.field, tc.value, validationError.Field, tc.wantErrorField)
			}

			if validationError.Message != tc.wantErrorMessage {
				t.Errorf("ValidateIdentifier(%q, %q): error message=%q, want %q", tc.field, tc.value, validationError.Message, tc.wantErrorMessage)
			}
		})
	}
}

// TestValidateTime checks UTC year bounds and PostgreSQL precision independently
// of storage, including offsets that cross the supported calendar boundary.
func TestValidateTime(t *testing.T) {
	cases := []struct {
		name        string
		field       string
		value       time.Time
		wantError   bool
		wantField   string
		wantMessage string
	}{
		{name: "last valid microsecond", field: "recorded_at", value: time.Date(9999, 12, 31, 23, 59, 59, 999_999_000, time.UTC)},
		{name: "nanosecond beyond microsecond", field: "recorded_at", value: time.Date(2026, 10, 1, 0, 0, 0, 1, time.UTC), wantError: true, wantField: "recorded_at", wantMessage: "Use at most microsecond precision."},
		{name: "missing time", field: "purchased_at", value: time.Time{}, wantError: true, wantField: "purchased_at", wantMessage: "timestamp must have a UTC year between 1000 and 9999"},
		{name: "offset below minimum", field: "effective_from", value: time.Date(1000, 1, 1, 0, 0, 0, 0, time.FixedZone("east", 3600)), wantError: true, wantField: "effective_from", wantMessage: "timestamp must have a UTC year between 1000 and 9999"},
		{name: "offset enters minimum year", field: "effective_from", value: time.Date(999, 12, 31, 23, 0, 0, 0, time.FixedZone("west", -3600))},
		{name: "offset exceeds maximum", field: "effective_from", value: time.Date(9999, 12, 31, 23, 59, 59, 0, time.FixedZone("west", -3600)), wantError: true, wantField: "effective_from", wantMessage: "timestamp must have a UTC year between 1000 and 9999"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTime(tc.field, tc.value)
			if (err != nil) != tc.wantError {
				t.Fatalf("ValidateTime(%q, %s): error=%v; wantError=%t", tc.field, tc.value, err, tc.wantError)
			}

			if !tc.wantError {
				return
			}

			var got *ValidationError
			if !errors.As(err, &got) {
				t.Fatalf("ValidateTime(%q, %s): error=%T; want *ValidationError", tc.field, tc.value, err)
			}

			if got.Field != tc.wantField || got.Message != tc.wantMessage {
				t.Errorf("ValidateTime(%q, %s): error=%+v; want field=%q message=%q", tc.field, tc.value, got, tc.wantField, tc.wantMessage)
			}
		})
	}
}

// TestValidateCents keeps zero prices and limits valid while credit grants must
// be positive; both modes reject negative cents before any financial write.
func TestValidateCents(t *testing.T) {
	cases := []struct {
		name        string
		field       string
		cents       int64
		positive    bool
		wantError   bool
		wantField   string
		wantMessage string
	}{
		{name: "zero price", field: "price_per_million_cents", cents: 0, positive: false},
		{name: "zero limit", field: "limit_cents", cents: 0, positive: false},
		{name: "one cent grant", field: "amount_cents", cents: 1, positive: true},
		{name: "zero grant", field: "amount_cents", cents: 0, positive: true, wantError: true, wantField: "amount_cents", wantMessage: "Invalid cents: 0."},
		{name: "negative price", field: "price_per_million_cents", cents: -1, positive: false, wantError: true, wantField: "price_per_million_cents", wantMessage: "Invalid cents: -1."},
		{name: "negative grant", field: "amount_cents", cents: -1, positive: true, wantError: true, wantField: "amount_cents", wantMessage: "Invalid cents: -1."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCents(tc.field, tc.cents, tc.positive)
			if (err != nil) != tc.wantError {
				t.Fatalf("ValidateCents(%q, %d, positive=%t): error=%v; wantError=%t", tc.field, tc.cents, tc.positive, err, tc.wantError)
			}

			if !tc.wantError {
				return
			}

			var got *ValidationError
			if !errors.As(err, &got) {
				t.Fatalf("ValidateCents(%q, %d): error=%T; want *ValidationError", tc.field, tc.cents, err)
			}

			if got.Field != tc.wantField || got.Message != tc.wantMessage {
				t.Errorf("ValidateCents(%q, %d): error=%+v; want field=%q message=%q", tc.field, tc.cents, got, tc.wantField, tc.wantMessage)
			}
		})
	}
}

// TestValidateDate permits midnight UTC and equivalent offset instants, while
// rejecting intra-day values and unsupported years independently of transport.
func TestValidateDate(t *testing.T) {
	cases := []struct {
		name      string
		input     time.Time
		wantError bool
		wantField string
	}{
		{name: "UTC midnight", input: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
		{name: "offset denotes UTC midnight", input: time.Date(2026, 11, 1, 8, 0, 0, 0, time.FixedZone("east", 8*3600))},
		{name: "one microsecond after midnight", input: time.Date(2026, 11, 1, 0, 0, 0, 1000, time.UTC), wantError: true, wantField: "effective_from"},
		{name: "noon", input: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC), wantError: true, wantField: "effective_from"},
		{name: "missing date", input: time.Time{}, wantError: true, wantField: "effective_from"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateDate("effective_from", tc.input)

			if (err != nil) != tc.wantError {
				t.Fatalf("ValidateDate(%s) error=%v; wantError=%t", tc.input, err, tc.wantError)
			}

			if tc.wantError {
				var validation *ValidationError
				if !errors.As(err, &validation) || validation.Field != tc.wantField {
					t.Errorf("ValidateDate(%s) error=%v; want field=%s", tc.input, err, tc.wantField)
				}
			}
		})
	}
}

// TestUTCDay resolves UTC midnight rather than local midnight, including calendar
// rollover at an offset and the last supported day with nanosecond clock input.
func TestUTCDay(t *testing.T) {
	cases := []struct {
		name  string
		input time.Time
		want  time.Time
	}{
		{name: "midday", input: time.Date(2026, 11, 1, 12, 34, 0, 0, time.UTC), want: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
		{name: "offset crosses UTC date", input: time.Date(2026, 11, 2, 0, 1, 0, 0, time.FixedZone("east", 8*3600)), want: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
		{name: "upper day", input: time.Date(9999, 12, 31, 23, 59, 59, 999_999_999, time.UTC), want: time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UTCDay(tc.input)

			if !got.Equal(tc.want) || got.Location() != time.UTC {
				t.Errorf("UTCDay(%s)=%s (%s); want %s (UTC)", tc.input, got, got.Location(), tc.want)
			}
		})
	}
}
