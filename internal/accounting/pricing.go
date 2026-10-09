package accounting

import (
	"fmt"
	"time"

	"e2b/billing-api/internal/usage"
)

// PriceVersion mirrors the catalog fields needed for historical rating.
// An empty CustomerID denotes a default price, matching SQL's NULL owner.
type PriceVersion struct {
	ID                   string
	CustomerID           string
	Metric               string
	EffectiveFrom        time.Time
	PricePerMillionCents int64
}

type Rating struct {
	Price      PriceVersion
	UsageMonth time.Time
	Charge     Amount
}

// Rate selects the latest eligible customer override before falling back to a
// default. One receipt must fit within one month and one effective price version.
// Unknown versions, missing prices, ambiguous catalogs, and crossing intervals
// return errors; the future worker preserves the receipt for investigation.
func Rate(event usage.Event, versions []PriceVersion) (Rating, error) {
	if err := event.Validate(); err != nil {
		return Rating{}, err
	}
	if event.SchemaVersion != 1 {
		return Rating{}, fmt.Errorf("unsupported usage schema version: %d", event.SchemaVersion)
	}
	month, err := UsageMonth(event.PeriodStart, event.PeriodEnd)
	if err != nil {
		return Rating{}, err
	}
	var selected *PriceVersion
	seen := make(map[string]bool)
	for _, version := range versions {
		if version.Metric != event.Metric || (version.CustomerID != "" && version.CustomerID != event.CustomerID) {
			continue
		}
		if _, err := UTCMonth(version.EffectiveFrom); err != nil || version.ID == "" || version.PricePerMillionCents < 0 {
			return Rating{}, fmt.Errorf("invalid price version: %s", version.ID)
		}
		key := version.CustomerID + "\x00" + version.EffectiveFrom.UTC().Format(time.RFC3339Nano)
		if seen[key] {
			return Rating{}, fmt.Errorf("ambiguous price versions at %s", version.EffectiveFrom)
		}
		seen[key] = true
		if version.EffectiveFrom.After(event.PeriodStart) {
			continue
		}
		if selected == nil || (version.CustomerID != "" && selected.CustomerID == "") ||
			(version.CustomerID == selected.CustomerID && version.EffectiveFrom.After(selected.EffectiveFrom)) {
			copy := version
			selected = &copy
		}
	}
	if selected == nil {
		return Rating{}, fmt.Errorf("no price for customer %s and metric %s at usage time", event.CustomerID, event.Metric)
	}
	for _, version := range versions {
		if version.Metric != event.Metric || (version.CustomerID != "" && version.CustomerID != event.CustomerID) ||
			!version.EffectiveFrom.After(event.PeriodStart) || !version.EffectiveFrom.Before(event.PeriodEnd) {
			continue
		}
		// Default changes are masked while an eligible override remains active.
		if selected.CustomerID == "" || version.CustomerID == selected.CustomerID {
			return Rating{}, fmt.Errorf("usage interval crosses a price version boundary")
		}
	}
	charge, err := UsageCharge(event.Units, selected.PricePerMillionCents)
	if err != nil {
		return Rating{}, err
	}
	return Rating{Price: *selected, UsageMonth: month, Charge: charge}, nil
}
