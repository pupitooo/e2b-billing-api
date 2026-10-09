package accounting_test

import (
	"testing"
	"time"

	"e2b/billing-api/internal/accounting"
)

// UTCMonth returns the UTC month boundary, including year one, and rejects
// missing timestamps and years beyond the supported transport range.
func TestUTCMonth(t *testing.T) {
	tests := []struct {
		name      string
		timestamp time.Time
		wantMonth string
		wantError bool
	}{
		{
			name:      "local November timestamp belongs to UTC October",
			timestamp: instant(t, "2026-11-01T07:00:00+08:00"),
			wantMonth: "2026-10-01T00:00:00Z",
			wantError: false,
		},
		{
			name:      "first supported year has a valid January boundary",
			timestamp: instant(t, "0001-01-02T12:00:00Z"),
			wantMonth: "0001-01-01T00:00:00Z",
			wantError: false,
		},
		{
			name:      "missing timestamp is rejected",
			timestamp: time.Time{},
			wantError: true,
		},
		{
			name:      "year beyond the transport range is rejected",
			timestamp: time.Date(10_000, 1, 1, 0, 0, 0, 0, time.UTC),
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := accounting.UTCMonth(tt.timestamp)
			if tt.wantError {
				if err == nil {
					t.Fatalf("UTCMonth(%s) error = nil; want an error", tt.timestamp)
				}
				return
			}
			if err != nil {
				t.Fatalf("UTCMonth(%s) error = %v; want nil", tt.timestamp, err)
			}
			if !got.Equal(instant(t, tt.wantMonth)) {
				t.Errorf("UTCMonth(%s) = %s; want %s", tt.timestamp, got, tt.wantMonth)
			}
			if got.Location() != time.UTC {
				t.Errorf("UTCMonth(%s) location = %s; want UTC", tt.timestamp, got.Location())
			}
		})
	}
}

// UsageMonth accepts a half-open interval within one UTC month, including an end
// at the next month's boundary; empty, reversed, crossing, or missing times fail.
func TestUsageMonth(t *testing.T) {
	tests := []struct {
		name      string
		start     string
		end       string
		wantMonth string
		wantError bool
	}{
		{
			name:      "offset interval ends exactly at the next UTC month",
			start:     "2026-11-01T07:00:00+08:00",
			end:       "2026-11-01T08:00:00+08:00",
			wantMonth: "2026-10-01T00:00:00Z",
			wantError: false,
		},
		{
			name:      "consumption in the first supported month is valid",
			start:     "0001-01-02T12:00:00Z",
			end:       "0001-01-02T13:00:00Z",
			wantMonth: "0001-01-01T00:00:00Z",
			wantError: false,
		},
		{
			name:      "equal start and end are rejected",
			start:     "2026-11-01T07:00:00+08:00",
			end:       "2026-11-01T07:00:00+08:00",
			wantError: true,
		},
		{
			name:      "end before start is rejected",
			start:     "2026-11-01T07:00:00+08:00",
			end:       "2026-11-01T06:59:59+08:00",
			wantError: true,
		},
		{
			name:      "one microsecond past the next UTC month is rejected",
			start:     "2026-11-01T07:00:00+08:00",
			end:       "2026-11-01T08:00:00.000001+08:00",
			wantError: true,
		},
		{
			name:      "missing end is rejected",
			start:     "2026-11-01T07:00:00+08:00",
			end:       "",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := accounting.UsageMonth(instant(t, tt.start), instant(t, tt.end))
			if tt.wantError {
				if err == nil {
					t.Fatalf("UsageMonth(%q, %q) error = nil; want an error", tt.start, tt.end)
				}
				return
			}
			if err != nil {
				t.Fatalf("UsageMonth(%q, %q) error = %v; want nil", tt.start, tt.end, err)
			}
			if !got.Equal(instant(t, tt.wantMonth)) {
				t.Errorf("UsageMonth(%q, %q) = %s; want %s", tt.start, tt.end, got, tt.wantMonth)
			}
			if got.Location() != time.UTC {
				t.Errorf("UsageMonth(%q, %q) location = %s; want UTC", tt.start, tt.end, got.Location())
			}
		})
	}
}

// BillingMonth preserves an open usage month and routes closed months to the
// first eligible open month; malformed inputs and exhausted calendar years fail.
func TestBillingMonth(t *testing.T) {
	tests := []struct {
		name         string
		usageMonth   string
		receivedAt   string
		closedMonths []string
		wantMonth    string
		wantError    bool
	}{
		{
			name:         "late usage retains its original open month",
			usageMonth:   "2026-10-01T00:00:00Z",
			receivedAt:   "2026-11-01T01:00:00Z",
			closedMonths: nil,
			wantMonth:    "2026-10-01T00:00:00Z",
			wantError:    false,
		},
		{
			name:         "closed October routes late usage to open November",
			usageMonth:   "2026-10-01T00:00:00Z",
			receivedAt:   "2026-11-01T01:00:00Z",
			closedMonths: []string{"2026-10-01T00:00:00Z"},
			wantMonth:    "2026-11-01T00:00:00Z",
			wantError:    false,
		},
		{
			name:         "closed October and November route usage to December",
			usageMonth:   "2026-10-01T00:00:00Z",
			receivedAt:   "2026-11-01T01:00:00Z",
			closedMonths: []string{"2026-10-01T00:00:00Z", "2026-11-01T00:00:00Z"},
			wantMonth:    "2026-12-01T00:00:00Z",
			wantError:    false,
		},
		{
			name:         "future usage never bills into an earlier receipt month",
			usageMonth:   "2026-11-01T00:00:00Z",
			receivedAt:   "2026-10-01T01:00:00Z",
			closedMonths: nil,
			wantMonth:    "2026-11-01T00:00:00Z",
			wantError:    false,
		},
		{
			name:         "closed usage month advances even when receipt is in that month",
			usageMonth:   "2026-10-01T00:00:00Z",
			receivedAt:   "2026-10-01T01:00:00Z",
			closedMonths: []string{"2026-10-01T00:00:00Z"},
			wantMonth:    "2026-11-01T00:00:00Z",
			wantError:    false,
		},
		{
			name:         "closed January in year one routes to February",
			usageMonth:   "0001-01-01T00:00:00Z",
			receivedAt:   "0001-01-02T12:00:00Z",
			closedMonths: []string{"0001-01-01T00:00:00Z"},
			wantMonth:    "0001-02-01T00:00:00Z",
			wantError:    false,
		},
		{
			name:         "usage month without a canonical boundary is rejected",
			usageMonth:   "2026-10-01T01:00:00Z",
			receivedAt:   "2026-11-01T00:00:00Z",
			closedMonths: nil,
			wantError:    true,
		},
		{
			name:         "closed month without a canonical boundary is rejected",
			usageMonth:   "2026-10-01T00:00:00Z",
			receivedAt:   "2026-11-01T00:00:00Z",
			closedMonths: []string{"2026-10-01T01:00:00Z"},
			wantError:    true,
		},
		{
			name:         "missing durable receipt time is rejected",
			usageMonth:   "2026-10-01T00:00:00Z",
			receivedAt:   "",
			closedMonths: nil,
			wantError:    true,
		},
		{
			name:         "routing beyond the last supported month is rejected",
			usageMonth:   "9999-12-01T00:00:00Z",
			receivedAt:   "9999-12-01T00:00:00Z",
			closedMonths: []string{"9999-12-01T00:00:00Z"},
			wantError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			closedMonths := make([]time.Time, len(tt.closedMonths))
			for i, month := range tt.closedMonths {
				closedMonths[i] = instant(t, month)
			}
			got, err := accounting.BillingMonth(instant(t, tt.usageMonth), instant(t, tt.receivedAt), closedMonths)
			if tt.wantError {
				if err == nil {
					t.Fatalf("BillingMonth(%q, %q, %v) error = nil; want an error", tt.usageMonth, tt.receivedAt, tt.closedMonths)
				}
				return
			}
			if err != nil {
				t.Fatalf("BillingMonth(%q, %q, %v) error = %v; want nil", tt.usageMonth, tt.receivedAt, tt.closedMonths, err)
			}
			if !got.Equal(instant(t, tt.wantMonth)) {
				t.Errorf("BillingMonth(%q, %q, %v) = %s; want %s", tt.usageMonth, tt.receivedAt, tt.closedMonths, got, tt.wantMonth)
			}
		})
	}
}

// AddonCharge bills the full monthly price from the UTC purchase month onward,
// including year one, and rejects a noncanonical billing month or negative price.
func TestAddonCharge(t *testing.T) {
	tests := []struct {
		name         string
		purchasedAt  string
		billingMonth string
		priceCents   int64
		wantCents    int64
		wantError    bool
	}{
		{
			name:         "month before purchase has no charge",
			purchasedAt:  "2026-11-01T07:59:59+08:00",
			billingMonth: "2026-09-01T00:00:00Z",
			priceCents:   2_000,
			wantCents:    0,
			wantError:    false,
		},
		{
			name:         "purchase at UTC October end incurs the full October charge",
			purchasedAt:  "2026-11-01T07:59:59+08:00",
			billingMonth: "2026-10-01T00:00:00Z",
			priceCents:   2_000,
			wantCents:    2_000,
			wantError:    false,
		},
		{
			name:         "month after purchase also incurs the full charge",
			purchasedAt:  "2026-11-01T07:59:59+08:00",
			billingMonth: "2026-11-01T00:00:00Z",
			priceCents:   2_000,
			wantCents:    2_000,
			wantError:    false,
		},
		{
			name:         "purchase in year one incurs that month's full charge",
			purchasedAt:  "0001-01-02T12:00:00Z",
			billingMonth: "0001-01-01T00:00:00Z",
			priceCents:   2_000,
			wantCents:    2_000,
			wantError:    false,
		},
		{
			name:         "billing date after the month boundary is rejected",
			purchasedAt:  "2026-11-01T07:59:59+08:00",
			billingMonth: "2026-10-02T00:00:00Z",
			priceCents:   2_000,
			wantError:    true,
		},
		{
			name:         "negative monthly price is rejected",
			purchasedAt:  "2026-11-01T07:59:59+08:00",
			billingMonth: "2026-10-01T00:00:00Z",
			priceCents:   -1,
			wantError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := accounting.AddonCharge(instant(t, tt.purchasedAt), instant(t, tt.billingMonth), tt.priceCents)
			if tt.wantError {
				if err == nil {
					t.Fatalf("AddonCharge(%q, %q, %d) error = nil; want an error", tt.purchasedAt, tt.billingMonth, tt.priceCents)
				}
				return
			}
			if err != nil {
				t.Fatalf("AddonCharge(%q, %q, %d) error = %v; want nil", tt.purchasedAt, tt.billingMonth, tt.priceCents, err)
			}
			if got != tt.wantCents {
				t.Errorf("AddonCharge(%q, %q, %d) = %d cents; want %d cents", tt.purchasedAt, tt.billingMonth, tt.priceCents, got, tt.wantCents)
			}
		})
	}
}

// Parse explicit timestamp fixtures; an empty string deliberately represents a
// missing timestamp so rejection cases keep their input visible in the table.
func instant(t *testing.T, value string) time.Time {
	t.Helper()
	if value == "" {
		return time.Time{}
	}
	timestamp, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("Invalid timestamp fixture %q: %v", value, err)
	}
	return timestamp
}
