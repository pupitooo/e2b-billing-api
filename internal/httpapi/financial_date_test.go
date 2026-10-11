package httpapi

import (
	"errors"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
)

// TestCommandDate accepts supported Gregorian dates only and returns midnight UTC;
// malformed dates, timestamps, offsets, and unsupported years have field errors.
func TestCommandDate(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		field     string
		wantTime  time.Time
		wantError bool
		wantField string
	}{
		{name: "first supported date", input: "1000-01-01", field: "effective_from", wantTime: time.Date(1000, 1, 1, 0, 0, 0, 0, time.UTC)},
		{name: "last supported date", input: "9999-12-31", field: "purchased_at", wantTime: time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)},
		{name: "leap day", input: "2028-02-29", field: "purchased_at", wantTime: time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)},
		{name: "non-leap day", input: "2026-02-29", field: "purchased_at", wantError: true, wantField: "purchased_at"},
		{name: "previous year", input: "0999-12-31", field: "effective_from", wantError: true, wantField: "effective_from"},
		{name: "next year", input: "10000-01-01", field: "effective_from", wantError: true, wantField: "effective_from"},
		{name: "midnight timestamp", input: "2026-11-01T00:00:00Z", field: "effective_from", wantError: true, wantField: "effective_from"},
		{name: "offset timestamp", input: "2026-11-01T08:00:00+08:00", field: "purchased_at", wantError: true, wantField: "purchased_at"},
		{name: "unpadded month", input: "2026-1-01", field: "effective_from", wantError: true, wantField: "effective_from"},
		{name: "unpadded day", input: "2026-11-1", field: "purchased_at", wantError: true, wantField: "purchased_at"},
		{name: "missing date", input: "", field: "purchased_at", wantError: true, wantField: "purchased_at"},
		{name: "leading whitespace", input: " 2026-11-01", field: "effective_from", wantError: true, wantField: "effective_from"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := commandDate(tc.input, tc.field)

			if (err != nil) != tc.wantError {
				t.Fatalf("commandDate(%q, %q) error=%v; wantError=%t", tc.input, tc.field, err, tc.wantError)
			}

			if tc.wantError {
				var validation *billing.ValidationError
				if !errors.As(err, &validation) || validation.Field != tc.wantField {
					t.Fatalf("commandDate(%q, %q) error=%v; want field=%s", tc.input, tc.field, err, tc.wantField)
				}

				return
			}

			if !got.Equal(tc.wantTime) || got.Location() != time.UTC {
				t.Errorf("commandDate(%q, %q)=%s (%s); want %s (UTC)", tc.input, tc.field, got, got.Location(), tc.wantTime)
			}
		})
	}
}
