package accounting

import (
	"fmt"
	"time"

	"e2b/billing-api/internal/usage"
)

// UTCMonth returns the first instant of the timestamp's original UTC month.
func UTCMonth(timestamp time.Time) (time.Time, error) {
	utc := timestamp.UTC()
	if timestamp.IsZero() || utc.Year() < usage.MinUTCYear || utc.Year() > usage.MaxUTCYear {
		return time.Time{}, fmt.Errorf("timestamp must have a UTC year between %d and %d", usage.MinUTCYear, usage.MaxUTCYear)
	}

	return time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC), nil
}

// UsageMonth accepts one half-open segment wholly within one UTC month.
// Ending exactly at the next month's boundary is valid; crossing it is not.
func UsageMonth(start, end time.Time) (time.Time, error) {
	month, err := UTCMonth(start)
	if err != nil {
		return time.Time{}, err
	}

	if _, err := UTCMonth(end); err != nil {
		return time.Time{}, err
	}

	if !end.After(start) || end.After(month.AddDate(0, 1, 0)) {
		return time.Time{}, fmt.Errorf("usage interval must fit within one UTC month")
	}

	return month, nil
}

// BillingMonth preserves an open original month. Closed original months route
// to the first open month at or after both original usage and durable receipt.
// The future caller must read closure state under the same customer lock as rating.
func BillingMonth(usageMonth, receivedAt time.Time, closedMonths []time.Time) (time.Time, error) {
	original, err := canonicalMonth(usageMonth)
	if err != nil {
		return time.Time{}, fmt.Errorf("usage month must be a UTC month boundary")
	}

	receiptMonth, err := UTCMonth(receivedAt)
	if err != nil {
		return time.Time{}, err
	}

	closed := make(map[time.Time]bool, len(closedMonths))
	for _, month := range closedMonths {
		canonical, err := canonicalMonth(month)
		if err != nil {
			return time.Time{}, fmt.Errorf("closed months must be UTC month boundaries")
		}

		closed[canonical] = true
	}

	if !closed[original] {
		return original, nil
	}

	candidate := original
	if receiptMonth.After(candidate) {
		candidate = receiptMonth
	}

	for closed[candidate] {
		candidate = candidate.AddDate(0, 1, 0)
		if candidate.Year() > usage.MaxUTCYear {
			return time.Time{}, fmt.Errorf("no representable open billing month")
		}
	}

	return candidate, nil
}

// AddonCharge bills the full purchased price from the UTC purchase month onward.
// Proration, cancellation, and quantities are outside the assignment's contract.
func AddonCharge(purchasedAt, billingMonth time.Time, priceCents int64) (int64, error) {
	startMonth, err := UTCMonth(purchasedAt)
	if err != nil {
		return 0, err
	}

	month, err := canonicalMonth(billingMonth)
	if err != nil || priceCents < 0 {
		return 0, fmt.Errorf("add-on billing requires a UTC month boundary and non-negative cents")
	}

	if month.Before(startMonth) {
		return 0, nil
	}

	return priceCents, nil
}

// canonicalMonth requires a first-of-month UTC instant in the supported year range.
func canonicalMonth(month time.Time) (time.Time, error) {
	utc := month.UTC()
	canonical := time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC)
	if utc.Year() < usage.MinUTCYear || utc.Year() > usage.MaxUTCYear || !canonical.Equal(month) {
		return time.Time{}, fmt.Errorf("month must be a UTC month boundary")
	}

	return canonical, nil
}
