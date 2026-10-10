// Package accounting contains pure financial rules shared by future writers.
package accounting

import (
	"fmt"
	"math/big"
)

// TicksPerCent defines the fixed resolution of stored monetary amounts.
const TicksPerCent int64 = 1_000_000

// PriceUnitCount is the resource unit count covered by a current catalog price.
// Store this on each historical price version when introducing other bases:
// https://github.com/pupitooo/e2b-billing-api/issues/29
const PriceUnitCount int64 = 1_000_000

// Amount is an immutable, non-negative number of ticks. Its zero value is zero.
// Arithmetic uses arbitrary precision; no method mutates an input's big.Int.
type Amount struct {
	ticks big.Int
}

// FromTicks copies an exact integer amount supplied by a database or caller.
func FromTicks(ticks *big.Int) (Amount, error) {
	if ticks == nil || ticks.Sign() < 0 {
		return Amount{}, fmt.Errorf("ticks must be a non-negative integer")
	}

	var amount Amount
	amount.ticks.Set(ticks)

	return amount, nil
}

// FromCents converts non-negative whole cents without an intermediate overflow.
func FromCents(cents int64) (Amount, error) {
	if cents < 0 {
		return Amount{}, fmt.Errorf("cents must be non-negative")
	}

	var amount Amount
	amount.ticks.Mul(big.NewInt(cents), big.NewInt(TicksPerCent))

	return amount, nil
}

// UsageCharge converts units to ticks using the whole-cents-per-million price.
// The price unit count and ticks per cent currently cancel, but measure different
// things. Multiply before dividing to retain exact amounts below a whole cent.
func UsageCharge(units, pricePerMillionCents int64) (Amount, error) {
	if units < 0 || pricePerMillionCents < 0 {
		return Amount{}, fmt.Errorf("units and price must be non-negative")
	}

	var amount Amount
	amount.ticks.Mul(big.NewInt(units), big.NewInt(pricePerMillionCents))
	amount.ticks.Mul(&amount.ticks, big.NewInt(TicksPerCent))
	var remainder big.Int
	amount.ticks.QuoRem(&amount.ticks, big.NewInt(PriceUnitCount), &remainder)
	if remainder.Sign() != 0 {
		return Amount{}, fmt.Errorf("charge cannot be represented in whole ticks")
	}

	return amount, nil
}

// Ticks returns a copy, allowing callers to encode it without changing the amount.
func (a Amount) Ticks() *big.Int { return new(big.Int).Set(&a.ticks) }

func (a Amount) Add(b Amount) Amount {
	var sum Amount
	sum.ticks.Add(&a.ticks, &b.ticks)

	return sum
}

func (a Amount) Compare(b Amount) int { return a.ticks.Cmp(&b.ticks) }

func (a Amount) Subtract(b Amount) (Amount, error) {
	if a.Compare(b) < 0 {
		return Amount{}, fmt.Errorf("amount subtraction would be negative")
	}

	var difference Amount
	difference.ticks.Sub(&a.ticks, &b.ticks)

	return difference, nil
}

// RoundCents uses half-up on a cumulative amount, rejecting bigint overflow.
func (a Amount) RoundCents() (int64, error) {
	rounded := new(big.Int).Add(&a.ticks, big.NewInt(TicksPerCent/2))
	rounded.Quo(rounded, big.NewInt(TicksPerCent))
	if !rounded.IsInt64() {
		return 0, fmt.Errorf("rounded cents exceed the signed 64-bit range")
	}

	return rounded.Int64(), nil
}

type CreditAllocation struct {
	Used      Amount
	Remaining Amount
	NetCharge Amount
}

// AllocateCredit applies available credit to this new charge only, before rounding.
// Later grants never retroactively change allocations for earlier charges.
func AllocateCredit(charge, available Amount) CreditAllocation {
	used := charge
	if available.Compare(charge) < 0 {
		used = available
	}

	// Both subtractions are non-negative by construction.
	remaining, _ := available.Subtract(used)
	net, _ := charge.Subtract(used)

	return CreditAllocation{Used: used, Remaining: remaining, NetCharge: net}
}

// InvoiceCharge holds whole-cent presentation amounts for one frozen group.
// CreditCents is a positive deduction, so GrossCents - CreditCents = NetCents.
type InvoiceCharge struct {
	GrossCents  int64
	CreditCents int64
	NetCents    int64
}

// InvoiceAmounts rounds the group's gross and net totals, deriving the credit
// line as their difference so independently rounded lines cannot alter the total.
// The exact credit ledger remains in ticks; these cents are presentation only.
func InvoiceAmounts(gross, allocatedCredit Amount) (InvoiceCharge, error) {
	net, err := gross.Subtract(allocatedCredit)
	if err != nil {
		return InvoiceCharge{}, fmt.Errorf("allocated credit exceeds gross charge")
	}

	grossCents, err := gross.RoundCents()
	if err != nil {
		return InvoiceCharge{}, err
	}

	netCents, err := net.RoundCents()
	if err != nil {
		return InvoiceCharge{}, err
	}

	return InvoiceCharge{GrossCents: grossCents, CreditCents: grossCents - netCents, NetCents: netCents}, nil
}

// LimitReached compares exact original-month gross usage with a whole-cent
// limit. Nil means unlimited; zero is reached even before any usage arrives.
func LimitReached(gross Amount, limitCents *int64) (bool, error) {
	if limitCents == nil {
		return false, nil
	}

	limit, err := FromCents(*limitCents)
	if err != nil {
		return false, err
	}

	return gross.Compare(limit) >= 0, nil
}
