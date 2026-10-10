package billing

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateIdentifier verifies that identifiers are bounded by UTF-8 bytes,
// accepting the exact boundary and reporting the input field above that limit.
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
