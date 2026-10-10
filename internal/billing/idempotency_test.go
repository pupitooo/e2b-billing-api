package billing

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateIdempotencyKey covers the JSON token boundaries independently
// of HTTP parsing, preserving exact keys and rejecting unsafe characters.
func TestValidateIdempotencyKey(t *testing.T) {
	cases := []struct {
		name      string
		key       string
		wantError bool
		wantField string
	}{
		{name: "UUID", key: "550e8400-e29b-41d4-a716-446655440000"},
		{name: "lower visible character", key: "!"},
		{name: "upper visible character", key: "~"},
		{name: "exact maximum", key: strings.Repeat("a", 256)},
		{name: "above maximum", key: strings.Repeat("a", 257), wantError: true, wantField: "idempotency_key"},
		{name: "empty", wantError: true, wantField: "idempotency_key"},
		{name: "space below visible range", key: " ", wantError: true, wantField: "idempotency_key"},
		{name: "DEL above visible range", key: "\x7f", wantError: true, wantField: "idempotency_key"},
		{name: "NUL", key: "a\x00b", wantError: true, wantField: "idempotency_key"},
		{name: "newline", key: "a\nb", wantError: true, wantField: "idempotency_key"},
		{name: "Unicode", key: "ž", wantError: true, wantField: "idempotency_key"},
		{name: "invalid UTF-8", key: "\xff", wantError: true, wantField: "idempotency_key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateIdempotencyKey(tc.key)

			if (err != nil) != tc.wantError {
				t.Fatalf("ValidateIdempotencyKey(%q): error=%v; wantError=%t", tc.key, err, tc.wantError)
			}

			if tc.wantError {
				var validation *ValidationError
				if !errors.As(err, &validation) || validation.Field != tc.wantField {
					t.Errorf("ValidateIdempotencyKey(%q): error=%v; want field=%q", tc.key, err, tc.wantField)
				}
			}
		})
	}
}
