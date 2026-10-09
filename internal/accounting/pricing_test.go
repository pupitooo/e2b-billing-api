package accounting_test

import (
	"testing"
	"time"

	"e2b/billing-api/internal/accounting"
	"e2b/billing-api/internal/usage"
)

// Historical defaults and customer overrides must use consumption time, not
// receipt time or catalog order; a later default never overrides Acme's price.
func TestHistoricalPriceSelection(t *testing.T) {
	prices := assignmentPrices(t)
	for _, fixture := range []struct {
		customer, at, id, ticks string
	}{
		{"acme", "2026-10-10T12:00:00Z", "acme-oct", "400000000"},
		{"acme", "2026-11-03T12:00:00Z", "acme-oct", "400000000"},
		{"cyberdyne", "2026-10-10T12:00:00Z", "default-oct", "500000000"},
		{"cyberdyne", "2026-10-15T00:00:00Z", "default-mid-oct", "600000000"},
	} {
		rating, err := accounting.Rate(metering(t, fixture.customer, fixture.at, 100_000_000), prices)
		if err != nil || rating.Price.ID != fixture.id || rating.Charge.Ticks().String() != fixture.ticks {
			t.Fatalf("Historical rating: got %+v, %v; want %s at %s ticks", rating, err, fixture.id, fixture.ticks)
		}
	}
}

// Price changes strictly inside a segment are unsupported, while a change at
// its end is outside the half-open interval. Masked default changes are harmless.
func TestPriceBoundarySegments(t *testing.T) {
	prices := assignmentPrices(t)
	event := metering(t, "cyberdyne", "2026-10-14T23:30:00Z", 1_000)
	if _, err := accounting.Rate(event, prices); err == nil {
		t.Fatal("A receipt crossing the default price change must fail")
	}
	event.PeriodEnd = instant(t, "2026-10-15T00:00:00Z")
	if rating, err := accounting.Rate(event, prices); err != nil || rating.Price.ID != "default-oct" {
		t.Fatalf("Price change at interval end must preserve the previous price: %+v, %v", rating, err)
	}
	event.CustomerID = "acme"
	event.PeriodEnd = instant(t, "2026-10-15T00:30:00Z")
	if rating, err := accounting.Rate(event, prices); err != nil || rating.Price.ID != "acme-oct" {
		t.Fatalf("An override must mask the default change: %+v, %v", rating, err)
	}
	prices = append(prices, accounting.PriceVersion{
		ID: "acme-new", CustomerID: "acme", Metric: "cpu_seconds",
		EffectiveFrom: instant(t, "2026-10-15T00:00:00Z"), PricePerMillionCents: 3,
	})
	if _, err := accounting.Rate(event, prices); err == nil {
		t.Fatal("A receipt crossing a customer price change must fail")
	}
	event.CustomerID = "cyberdyne"
	prices = append(prices, accounting.PriceVersion{
		ID: "cyberdyne-new", CustomerID: "cyberdyne", Metric: "cpu_seconds",
		EffectiveFrom: instant(t, "2026-10-15T00:00:00Z"), PricePerMillionCents: 2,
	})
	if _, err := accounting.Rate(event, prices); err == nil {
		t.Fatal("An override beginning inside a default-priced receipt must fail")
	}
}

// Missing, ambiguous, and malformed relevant prices, unsupported schemas, and
// cross-month events are visible rating errors, rather than free consumption.
func TestRatingRejections(t *testing.T) {
	event := metering(t, "cyberdyne", "2026-10-10T12:00:00Z", 1)
	prices := assignmentPrices(t)
	if _, err := accounting.Rate(event, nil); err == nil {
		t.Fatal("Missing historical prices must fail")
	}
	duplicate := append(append([]accounting.PriceVersion{}, prices...), prices[0])
	if _, err := accounting.Rate(event, duplicate); err == nil {
		t.Fatal("Ambiguous price versions must fail")
	}
	for _, malformed := range []accounting.PriceVersion{
		{ID: "bad", Metric: "cpu_seconds", EffectiveFrom: event.PeriodStart, PricePerMillionCents: -1},
		{Metric: "cpu_seconds", EffectiveFrom: event.PeriodStart, PricePerMillionCents: 5},
		{ID: "bad", Metric: "cpu_seconds", PricePerMillionCents: 5},
	} {
		if _, err := accounting.Rate(event, []accounting.PriceVersion{malformed}); err == nil {
			t.Fatal("Invalid relevant price data must fail")
		}
	}
	event.SchemaVersion = 2
	if _, err := accounting.Rate(event, prices); err == nil {
		t.Fatal("Unsupported schema versions must fail")
	}
	event = metering(t, "cyberdyne", "2026-10-31T23:30:00Z", 1)
	if _, err := accounting.Rate(event, prices); err == nil {
		t.Fatal("A receipt spanning UTC months must fail")
	}
	event = metering(t, "cyberdyne", "2026-09-30T12:00:00Z", 1)
	if _, err := accounting.Rate(event, prices); err == nil {
		t.Fatal("Usage before any applicable price must fail")
	}
}

// The assignment's pure calculations must yield the required credit balances
// and invoice totals, including late October usage and credit-excluded add-ons.
func TestAssignmentFinancialResults(t *testing.T) {
	prices := assignmentPrices(t)
	credit := ticks(t, "2500000000")
	var octoberGross, octoberCredit accounting.Amount
	for _, event := range []usage.Event{
		metering(t, "acme", "2026-10-10T12:00:00Z", 100_000_000),
		metering(t, "acme", "2026-10-20T12:00:00Z", 200_000_000),
	} {
		rating, err := accounting.Rate(event, prices)
		if err != nil {
			t.Fatal(err)
		}
		allocation := accounting.AllocateCredit(rating.Charge, credit)
		credit = allocation.Remaining
		octoberGross = octoberGross.Add(rating.Charge)
		octoberCredit = octoberCredit.Add(allocation.Used)
	}
	october, err := accounting.InvoiceAmounts(octoberGross, octoberCredit)
	if err != nil || october.GrossCents != 1_200 || october.CreditCents != 1_200 ||
		october.NetCents+2_000 != 2_000 || credit.Ticks().String() != "1300000000" {
		t.Fatalf("Acme October must total 20 USD with 13 USD credit left: %+v, %s, %v", october, credit.Ticks(), err)
	}
	var novemberGross, novemberCredit accounting.Amount
	for _, event := range []usage.Event{
		metering(t, "acme", "2026-10-30T12:00:00Z", 50_000_000),
		metering(t, "acme", "2026-11-03T12:00:00Z", 100_000_000),
	} {
		rating, err := accounting.Rate(event, prices)
		if err != nil {
			t.Fatal(err)
		}
		allocation := accounting.AllocateCredit(rating.Charge, credit)
		credit = allocation.Remaining
		novemberGross = novemberGross.Add(rating.Charge)
		novemberCredit = novemberCredit.Add(allocation.Used)
	}
	november, err := accounting.InvoiceAmounts(novemberGross, novemberCredit)
	if err != nil || november.GrossCents != 600 || november.CreditCents != 600 ||
		november.NetCents+2_000 != 2_000 || credit.Ticks().String() != "700000000" {
		t.Fatalf("Acme November must total 20 USD with 7 USD credit left: %+v, %s, %v", november, credit.Ticks(), err)
	}
	var cyberdyneCents int64
	var cyberdyneGross accounting.Amount
	for _, event := range []usage.Event{
		metering(t, "cyberdyne", "2026-10-10T12:00:00Z", 123_456_789),
		metering(t, "cyberdyne", "2026-10-20T12:00:00Z", 200_000_000),
	} {
		rating, err := accounting.Rate(event, prices)
		if err != nil {
			t.Fatal(err)
		}
		line, err := accounting.InvoiceAmounts(rating.Charge, accounting.Amount{})
		if err != nil {
			t.Fatal(err)
		}
		cyberdyneCents += line.NetCents
		cyberdyneGross = cyberdyneGross.Add(rating.Charge)
	}
	limit, err := accounting.FromCents(1_500)
	if err != nil || cyberdyneCents != 1_817 || cyberdyneGross.Compare(limit) < 0 {
		t.Fatalf("Cyberdyne must owe 18.17 USD and reach the 15 USD gross limit: %d, %v", cyberdyneCents, err)
	}
}

// Reproduce the assignment catalog in deliberately unsorted order for pure tests.
func assignmentPrices(t *testing.T) []accounting.PriceVersion {
	t.Helper()
	return []accounting.PriceVersion{
		{ID: "default-mid-oct", Metric: "cpu_seconds", EffectiveFrom: instant(t, "2026-10-15T00:00:00Z"), PricePerMillionCents: 6},
		{ID: "acme-oct", CustomerID: "acme", Metric: "cpu_seconds", EffectiveFrom: instant(t, "2026-10-01T00:00:00Z"), PricePerMillionCents: 4},
		{ID: "default-oct", Metric: "cpu_seconds", EffectiveFrom: instant(t, "2026-10-01T00:00:00Z"), PricePerMillionCents: 5},
	}
}

// Build a valid one-hour metering increment, leaving receipt time outside rating.
func metering(t *testing.T, customer, at string, units int64) usage.Event {
	t.Helper()
	start := instant(t, at)
	return usage.Event{
		Source: "accounting-test", EventID: "event", SchemaVersion: 1,
		CustomerID: customer, SandboxID: "sandbox", Metric: "cpu_seconds",
		PeriodStart: start, PeriodEnd: start.Add(time.Hour), Units: units,
	}
}
