// Package billingclock selects the business clock independently of runtime timers.
package billingclock

import (
	"fmt"
	"time"

	"e2b/billing-api/internal/billing"
)

// RealNow is the single source of wall time for business operations. Runtime
// deadlines, worker heartbeats, and retry timers keep their own real clocks.
func RealNow() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// New selects real time or a fixed, supported UTC instant for a demo process.
// The returned provider is sampled by operations, never frozen at real startup.
func New(value string) (func() time.Time, error) {
	if value == "" {
		return RealNow, nil
	}

	instant, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, fmt.Errorf("E2B_DEMO_NOW must be a supported RFC3339 timestamp: %w", err)
	}

	instant = instant.UTC()
	if err := billing.ValidateTime("E2B_DEMO_NOW", instant); err != nil {
		return nil, fmt.Errorf("invalid E2B_DEMO_NOW: %w", err)
	}

	return func() time.Time { return instant }, nil
}
