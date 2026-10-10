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
// return errors; the worker preserves the receipt for investigation.
func Rate(event usage.Event, versions []PriceVersion) (Rating, error) {
	if err := event.Validate(); err != nil {
		return Rating{}, err
	}

	if event.SchemaVersion != usage.SchemaVersionV1 {
		return Rating{}, fmt.Errorf("unsupported usage schema version: %d", event.SchemaVersion)
	}

	month, err := UsageMonth(event.PeriodStart, event.PeriodEnd)
	if err != nil {
		return Rating{}, err
	}

	prices, err := applicablePrices(event, versions)
	if err != nil {
		return Rating{}, err
	}

	selected, err := latestEligiblePrice(event, prices)
	if err != nil {
		return Rating{}, err
	}

	if crossesPriceBoundary(event, selected, prices) {
		return Rating{}, fmt.Errorf("usage interval crosses a price version boundary")
	}

	charge, err := UsageCharge(event.Units, selected.PricePerMillionCents)
	if err != nil {
		return Rating{}, err
	}

	return Rating{Price: selected, UsageMonth: month, Charge: charge}, nil
}

// Validate all relevant versions, including future ones, before selecting a
// price. Invalid or duplicate catalog entries must never be silently ignored.
func applicablePrices(event usage.Event, versions []PriceVersion) ([]PriceVersion, error) {
	var prices []PriceVersion
	seen := make(map[string]bool)
	for _, version := range versions {
		if version.Metric != event.Metric || (version.CustomerID != "" && version.CustomerID != event.CustomerID) {
			continue
		}

		if _, err := UTCMonth(version.EffectiveFrom); err != nil || version.ID == "" || version.PricePerMillionCents < 0 {
			return nil, fmt.Errorf("invalid price version: %s", version.ID)
		}

		key := version.CustomerID + "\x00" + version.EffectiveFrom.UTC().Format(time.RFC3339Nano)
		if seen[key] {
			return nil, fmt.Errorf("ambiguous price versions at %s", version.EffectiveFrom)
		}

		seen[key] = true
		prices = append(prices, version)
	}

	return prices, nil
}

func latestEligiblePrice(event usage.Event, prices []PriceVersion) (PriceVersion, error) {
	var selected PriceVersion
	var found bool
	for _, version := range prices {
		if version.EffectiveFrom.After(event.PeriodStart) {
			continue
		}

		if !found || priceTakesPriority(version, selected) {
			selected, found = version, true
		}
	}

	if !found {
		return PriceVersion{}, fmt.Errorf("no price for customer %s and metric %s at usage time", event.CustomerID, event.Metric)
	}

	return selected, nil
}

func priceTakesPriority(candidate, current PriceVersion) bool {
	if candidate.CustomerID != current.CustomerID {
		return candidate.CustomerID != ""
	}

	return candidate.EffectiveFrom.After(current.EffectiveFrom)
}

func crossesPriceBoundary(event usage.Event, selected PriceVersion, prices []PriceVersion) bool {
	for _, version := range prices {
		if !version.EffectiveFrom.After(event.PeriodStart) || !version.EffectiveFrom.Before(event.PeriodEnd) {
			continue
		}

		// Default changes are masked while an eligible override remains active.
		if selected.CustomerID == "" || version.CustomerID == selected.CustomerID {
			return true
		}
	}

	return false
}
