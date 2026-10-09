package accounting_test

import (
	"testing"
	"time"

	"e2b/billing-api/internal/accounting"
)

// Offset timestamps must preserve the UTC usage month, including a segment that
// ends exactly at the next UTC month's boundary. Crossing segments are errors.
func TestUsageMonthUTC(t *testing.T) {
	start := instant(t, "2026-11-01T07:00:00+08:00")
	end := instant(t, "2026-11-01T08:00:00+08:00")
	month, err := accounting.UsageMonth(start, end)
	if err != nil || !month.Equal(instant(t, "2026-10-01T00:00:00Z")) || month.Location() != time.UTC {
		t.Fatalf("UTC October month expected: %s, %v", month, err)
	}
	for _, badEnd := range []time.Time{start, start.Add(-time.Second), end.Add(time.Microsecond), {}} {
		if _, err := accounting.UsageMonth(start, badEnd); err == nil {
			t.Fatalf("Invalid or crossing interval ending %s must fail", badEnd)
		}
	}
	if _, err := accounting.UTCMonth(time.Date(10_000, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("An unrepresentable transport year must fail")
	}
}

// Late usage retains its original open month, or moves beyond closed months
// without changing its usage month. Future usage never bills into an earlier month.
func TestBillingMonthRouting(t *testing.T) {
	october := instant(t, "2026-10-01T00:00:00Z")
	november := instant(t, "2026-11-01T00:00:00Z")
	december := instant(t, "2026-12-01T00:00:00Z")
	for _, fixture := range []struct {
		usage, receipt, want time.Time
		closed               []time.Time
	}{
		{october, november.Add(time.Hour), october, nil},
		{october, november.Add(time.Hour), november, []time.Time{october}},
		{october, november.Add(time.Hour), december, []time.Time{october, november}},
		{november, october.Add(time.Hour), november, nil},
		{october, october.Add(time.Hour), november, []time.Time{october}},
	} {
		got, err := accounting.BillingMonth(fixture.usage, fixture.receipt, fixture.closed)
		if err != nil || !got.Equal(fixture.want) {
			t.Fatalf("Billing month: got %s, %v; want %s", got, err, fixture.want)
		}
	}
	if _, err := accounting.BillingMonth(october.Add(time.Hour), november, nil); err == nil {
		t.Fatal("A usage month must be a canonical UTC month boundary")
	}
	if _, err := accounting.BillingMonth(october, november, []time.Time{october.Add(time.Hour)}); err == nil {
		t.Fatal("A closed month must be a canonical UTC month boundary")
	}
	if _, err := accounting.BillingMonth(october, time.Time{}, nil); err == nil {
		t.Fatal("Missing durable receipt time must fail")
	}
	lastMonth := instant(t, "9999-12-01T00:00:00Z")
	if _, err := accounting.BillingMonth(lastMonth, lastMonth, []time.Time{lastMonth}); err == nil {
		t.Fatal("Routing past the last representable year must fail")
	}
}

// An add-on purchased at UTC month end bills the full price in that month and
// later months, regardless of its local offset, with no charge in earlier months.
func TestAddonFullMonthlyCharge(t *testing.T) {
	purchase := instant(t, "2026-11-01T07:59:59+08:00")
	for _, fixture := range []struct {
		month string
		want  int64
	}{
		{"2026-09-01T00:00:00Z", 0}, {"2026-10-01T00:00:00Z", 2_000}, {"2026-11-01T00:00:00Z", 2_000},
	} {
		got, err := accounting.AddonCharge(purchase, instant(t, fixture.month), 2_000)
		if err != nil || got != fixture.want {
			t.Fatalf("Add-on month %s: got %d, %v; want %d", fixture.month, got, err, fixture.want)
		}
	}
	if _, err := accounting.AddonCharge(purchase, instant(t, "2026-10-02T00:00:00Z"), 2_000); err == nil {
		t.Fatal("A billing date that is not a month boundary must fail")
	}
	if _, err := accounting.AddonCharge(purchase, instant(t, "2026-10-01T00:00:00Z"), -1); err == nil {
		t.Fatal("A negative add-on price must fail")
	}
}

// The first supported calendar month equals Go's zero time but must still route
// and bill correctly when derived from a valid non-zero consumption timestamp.
func TestFirstRepresentableMonth(t *testing.T) {
	start := instant(t, "0001-01-02T12:00:00Z")
	month, err := accounting.UsageMonth(start, start.Add(time.Hour))
	if err != nil || !month.Equal(instant(t, "0001-01-01T00:00:00Z")) {
		t.Fatalf("First-year January must be a valid usage month: %s, %v", month, err)
	}
	billingMonth, err := accounting.BillingMonth(month, start, []time.Time{month})
	if err != nil || !billingMonth.Equal(instant(t, "0001-02-01T00:00:00Z")) {
		t.Fatalf("Closed first-year January must route to February: %s, %v", billingMonth, err)
	}
	if cents, err := accounting.AddonCharge(start, month, 2_000); err != nil || cents != 2_000 {
		t.Fatalf("A first-year January purchase must charge its month: %d, %v", cents, err)
	}
}

// Parse timestamp fixtures with their explicit offsets, preserving the same instant.
func instant(t *testing.T, value string) time.Time {
	t.Helper()
	timestamp, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return timestamp
}
