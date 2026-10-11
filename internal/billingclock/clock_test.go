package billingclock_test

import (
	"strings"
	"testing"
	"time"

	"e2b/billing-api/internal/billingclock"
)

// TestNew selects a live provider or a repeatable fixed instant, normalizes UTC,
// and rejects unsupported calendar and precision boundaries before startup.
func TestNew(t *testing.T) {
	cases := []struct {
		name, input, wantTime, wantErrorText string
		wantError                            bool
	}{
		{name: "empty setting reads current time on every call", input: "", wantError: false},
		{name: "fixed UTC month boundary", input: "2026-11-01T00:00:00Z", wantTime: "2026-11-01T00:00:00Z", wantError: false},
		{name: "offset resolves to the same UTC month boundary", input: "2026-11-01T08:00:00+08:00", wantTime: "2026-11-01T00:00:00Z", wantError: false},
		{name: "minimum UTC year", input: "1000-01-01T00:00:00Z", wantTime: "1000-01-01T00:00:00Z", wantError: false},
		{name: "maximum year and microsecond precision", input: "9999-12-31T23:59:59.999999Z", wantTime: "9999-12-31T23:59:59.999999Z", wantError: false},
		{name: "offset moves below the minimum UTC year", input: "1000-01-01T00:00:00+01:00", wantError: true, wantErrorText: "UTC year between 1000 and 9999"},
		{name: "offset moves beyond the maximum UTC year", input: "9999-12-31T23:59:59-01:00", wantError: true, wantErrorText: "UTC year between 1000 and 9999"},
		{name: "nanosecond precision is rejected", input: "2026-11-01T00:00:00.000000001Z", wantError: true, wantErrorText: "microsecond precision"},
		{name: "timestamp requires an offset", input: "2026-11-01T00:00:00", wantError: true, wantErrorText: "E2B_DEMO_NOW"},
		{name: "date alone is rejected", input: "2026-11-01", wantError: true, wantErrorText: "E2B_DEMO_NOW"},
		{name: "whitespace is not an unset value", input: " ", wantError: true, wantErrorText: "E2B_DEMO_NOW"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now, err := billingclock.New(tc.input)
			if (err != nil) != tc.wantError {
				t.Fatalf("New(%q) error = %v; wantError=%t", tc.input, err, tc.wantError)
			}

			if tc.wantError {
				if !strings.Contains(err.Error(), tc.wantErrorText) {
					t.Errorf("New(%q) error = %q; want text %q", tc.input, err, tc.wantErrorText)
				}

				if now != nil {
					t.Errorf("New(%q) provider is nonnil; want nil", tc.input)
				}

				return
			}

			for call := 0; call < 2; call++ {
				before := time.Now().UTC().Truncate(time.Microsecond)
				got := now()
				after := time.Now().UTC()

				if got.Location() != time.UTC || got.Nanosecond()%1_000 != 0 {
					t.Errorf("New(%q) call %d time = %s; want UTC microsecond precision", tc.input, call, got)
				}

				if tc.wantTime != "" {
					if got.Format(time.RFC3339Nano) != tc.wantTime {
						t.Errorf("New(%q) call %d time = %s; want %s", tc.input, call, got, tc.wantTime)
					}
				} else if got.Before(before) || got.After(after) {
					t.Errorf("New(%q) call %d time = %s; want current time between %s and %s", tc.input, call, got, before, after)
				}
			}
		})
	}
}
