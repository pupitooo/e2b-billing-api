package accounting_test

import (
	"math"
	"math/big"
	"testing"

	"e2b/billing-api/internal/accounting"
)

// Large metering products and cent conversions must stay exact beyond bigint.
func TestMoneyPrecision(t *testing.T) {
	charge, err := accounting.UsageCharge(math.MaxInt64, math.MaxInt64)
	if err != nil || charge.Ticks().String() != "85070591730234615847396907784232501249" {
		t.Fatalf("Large price product lost precision: %s, %v", charge.Ticks(), err)
	}
	cents, err := accounting.FromCents(math.MaxInt64)
	if err != nil || cents.Ticks().String() != "9223372036854775807000000" {
		t.Fatalf("Cent conversion lost precision: %s, %v", cents.Ticks(), err)
	}
	if rounded, err := cents.RoundCents(); err != nil || rounded != math.MaxInt64 {
		t.Fatalf("Maximum whole cents must round unchanged: %d, %v", rounded, err)
	}
}

// Half-cent boundaries round upward, while a rounded cent overflow is rejected.
func TestMoneyRounding(t *testing.T) {
	for _, fixture := range []struct {
		ticks string
		cents int64
	}{
		{"0", 0}, {"5000", 0}, {"499999", 0}, {"500000", 1},
		{"999999", 1}, {"1000000", 1}, {"1500000", 2}, {"617283945", 617},
		{"9223372036854775807499999", math.MaxInt64},
	} {
		got, err := ticks(t, fixture.ticks).RoundCents()
		if err != nil || got != fixture.cents {
			t.Fatalf("Rounding %s ticks: got %d, %v; want %d", fixture.ticks, got, err, fixture.cents)
		}
	}
	if _, err := ticks(t, "9223372036854775807500000").RoundCents(); err == nil {
		t.Fatal("A rounded amount above MaxInt64 cents must fail")
	}
}

// Caller-owned integers, copied values, and returned integers cannot mutate money.
func TestMoneyImmutability(t *testing.T) {
	input := big.NewInt(500_000)
	amount, err := accounting.FromTicks(input)
	if err != nil {
		t.Fatal(err)
	}
	input.SetInt64(0)
	amount.Ticks().SetInt64(0)
	copy := amount
	sum := copy.Add(amount)
	if amount.Ticks().Int64() != 500_000 || copy.Ticks().Int64() != 500_000 || sum.Ticks().Int64() != 1_000_000 {
		t.Fatal("Exact amounts must not share mutable arithmetic with callers")
	}
	if _, err := amount.Subtract(sum); err == nil {
		t.Fatal("Subtraction cannot create a negative amount")
	}
	if (accounting.Amount{}).Ticks().Sign() != 0 {
		t.Fatal("The zero value must represent zero ticks")
	}
}

// Negative or absent monetary inputs are errors; free prices and zero units are valid.
func TestMoneyValidation(t *testing.T) {
	if _, err := accounting.FromTicks(nil); err == nil {
		t.Fatal("Nil tick input must fail")
	}
	if _, err := accounting.FromTicks(big.NewInt(-1)); err == nil {
		t.Fatal("Negative ticks must fail")
	}
	if _, err := accounting.FromCents(-1); err == nil {
		t.Fatal("Negative cents must fail")
	}
	for _, fixture := range [][2]int64{{-1, 5}, {1, -1}} {
		if _, err := accounting.UsageCharge(fixture[0], fixture[1]); err == nil {
			t.Fatal("Negative units or prices must fail")
		}
	}
	for _, fixture := range [][2]int64{{0, 5}, {1, 0}} {
		charge, err := accounting.UsageCharge(fixture[0], fixture[1])
		if err != nil || charge.Ticks().Sign() != 0 {
			t.Fatalf("A zero quantity or free price must produce zero: %s, %v", charge.Ticks(), err)
		}
	}
}

// One thousand sub-cent events consume exactly the same credit as one combined
// event; partially used credit retains its fractional-cent balance between events.
func TestCreditBeforeRoundingAndEventSplitting(t *testing.T) {
	initial := ticks(t, "1000000")
	first := accounting.AllocateCredit(ticks(t, "5000"), initial)
	if first.Used.Ticks().Int64() != 5_000 || first.Remaining.Ticks().Int64() != 995_000 || first.NetCharge.Ticks().Sign() != 0 {
		t.Fatal("A small charge must debit 5,000 ticks and preserve 995,000 ticks")
	}
	remaining := initial
	var gross, used, net accounting.Amount
	for i := 0; i < 1000; i++ {
		charge, err := accounting.UsageCharge(1000, 5)
		if err != nil {
			t.Fatal(err)
		}
		allocation := accounting.AllocateCredit(charge, remaining)
		remaining = allocation.Remaining
		gross = gross.Add(charge)
		used = used.Add(allocation.Used)
		net = net.Add(allocation.NetCharge)
	}
	combinedCharge, err := accounting.UsageCharge(1_000_000, 5)
	if err != nil {
		t.Fatal(err)
	}
	combined := accounting.AllocateCredit(combinedCharge, initial)
	if gross.Compare(combinedCharge) != 0 || used.Compare(combined.Used) != 0 ||
		net.Compare(combined.NetCharge) != 0 || remaining.Compare(combined.Remaining) != 0 {
		t.Fatal("Batch and sandbox splitting must not change one group's financial result")
	}
	invoice, err := accounting.InvoiceAmounts(gross, used)
	if err != nil || invoice != (accounting.InvoiceCharge{GrossCents: 5, CreditCents: 1, NetCents: 4}) {
		t.Fatalf("Unexpected grouped invoice amounts: %+v, %v", invoice, err)
	}
}

// Credit received after an uncovered charge pays new consumption only; earlier
// allocations and the original gross charge remain unchanged.
func TestLaterCreditGrant(t *testing.T) {
	charge := ticks(t, "1000000")
	first := accounting.AllocateCredit(charge, accounting.Amount{})
	second := accounting.AllocateCredit(charge, ticks(t, "2000000"))
	if first.Used.Ticks().Sign() != 0 || first.NetCharge.Compare(charge) != 0 ||
		second.Used.Compare(charge) != 0 || second.Remaining.Compare(charge) != 0 {
		t.Fatal("A later grant cannot change an earlier allocation")
	}
}

// Separately rounding an exact credit can disagree with rounding net usage.
// Deriving the credit presentation from gross and net guarantees balanced lines.
func TestInvoiceCreditPresentation(t *testing.T) {
	for _, fixture := range []struct {
		gross, credit string
		want          accounting.InvoiceCharge
	}{
		{"1000000", "500000", accounting.InvoiceCharge{GrossCents: 1, CreditCents: 0, NetCents: 1}},
		{"1500000", "500000", accounting.InvoiceCharge{GrossCents: 2, CreditCents: 1, NetCents: 1}},
		{"5000", "5000", accounting.InvoiceCharge{}},
		{"1000000000", "700000000", accounting.InvoiceCharge{GrossCents: 1000, CreditCents: 700, NetCents: 300}},
	} {
		got, err := accounting.InvoiceAmounts(ticks(t, fixture.gross), ticks(t, fixture.credit))
		if err != nil || got != fixture.want || got.GrossCents-got.CreditCents != got.NetCents {
			t.Fatalf("Gross %s and credit %s: got %+v, %v; want %+v", fixture.gross, fixture.credit, got, err, fixture.want)
		}
	}
	if _, err := accounting.InvoiceAmounts(ticks(t, "1"), ticks(t, "2")); err == nil {
		t.Fatal("Allocated credit cannot exceed exact gross usage")
	}
	if _, err := accounting.InvoiceAmounts(ticks(t, "9223372036854775807500000"), accounting.Amount{}); err == nil {
		t.Fatal("An invoice line above bigint cents must fail")
	}
}

// Gross spend must be compared before rounding; nil, zero, and invalid limits
// retain distinct semantics even when the displayed spend is one cent.
func TestExactSpendLimit(t *testing.T) {
	for _, fixture := range []struct {
		gross string
		limit int64
		want  bool
	}{
		{"0", 0, true}, {"999999", 1, false}, {"1000000", 1, true},
		{"617283945", 1500, false}, {"1817283945", 1500, true},
	} {
		got, err := accounting.LimitReached(ticks(t, fixture.gross), &fixture.limit)
		if err != nil || got != fixture.want {
			t.Fatalf("Gross %s with limit %d: got %v, %v; want %v", fixture.gross, fixture.limit, got, err, fixture.want)
		}
	}
	if got, err := accounting.LimitReached(ticks(t, "1000000"), nil); err != nil || got {
		t.Fatal("An absent limit must remain unlimited")
	}
	negative := int64(-1)
	if _, err := accounting.LimitReached(accounting.Amount{}, &negative); err == nil {
		t.Fatal("Negative spend limits must fail")
	}
}

// Parse literal tick fixtures without float conversions or unchecked constructors.
func ticks(t *testing.T, value string) accounting.Amount {
	t.Helper()
	integer, ok := new(big.Int).SetString(value, 10)
	if !ok {
		t.Fatalf("Invalid tick fixture: %s", value)
	}
	amount, err := accounting.FromTicks(integer)
	if err != nil {
		t.Fatal(err)
	}
	return amount
}
