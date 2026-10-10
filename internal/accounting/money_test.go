package accounting_test

import (
	"math"
	"math/big"
	"testing"

	"e2b/billing-api/internal/accounting"
)

// FromTicks accepts non-negative integers, rejects nil and negative values, and
// copies caller-owned input so later mutations cannot change the returned amount.
func TestFromTicks(t *testing.T) {
	tests := []struct {
		name      string
		input     *big.Int
		wantTicks string
		wantError bool
	}{
		{
			name:  "zero ticks are valid",
			input: big.NewInt(0), wantTicks: "0", wantError: false,
		},
		{
			name:  "fractional cent ticks remain exact",
			input: big.NewInt(500_000), wantTicks: "500000", wantError: false,
		},
		{
			name:  "missing integer is rejected",
			input: nil, wantError: true,
		},
		{
			name:  "negative ticks are rejected",
			input: big.NewInt(-1), wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := accounting.FromTicks(tt.input)
			if tt.wantError {
				if err == nil {
					t.Fatalf("FromTicks(%v) error = nil; want an error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("FromTicks(%v) error = %v; want nil", tt.input, err)
			}
			if got.Ticks().String() != tt.wantTicks {
				t.Errorf("FromTicks(%v) = %s ticks; want %s ticks", tt.input, got.Ticks(), tt.wantTicks)
			}
		})
	}

	t.Run("changing the caller's integer preserves the constructed amount", func(t *testing.T) {
		input := big.NewInt(500_000)
		mutateInputTo := int64(0)
		wantTicks := "500000"

		got, err := accounting.FromTicks(input)
		if err != nil {
			t.Fatalf("FromTicks(%s) error = %v; want nil", input, err)
		}
		input.SetInt64(mutateInputTo)
		if got.Ticks().String() != wantTicks {
			t.Errorf("FromTicks result after input changed to %d = %s ticks; want %s ticks", mutateInputTo, got.Ticks(), wantTicks)
		}
	})
}

// FromCents converts whole cents to exact ticks without int64 multiplication
// overflow; zero is valid and negative cents are rejected.
func TestFromCents(t *testing.T) {
	tests := []struct {
		name      string
		cents     int64
		wantTicks string
		wantError bool
	}{
		{
			name:  "zero cents remain zero",
			cents: 0, wantTicks: "0", wantError: false,
		},
		{
			name:  "one cent contains one million ticks",
			cents: 1, wantTicks: "1000000", wantError: false,
		},
		{
			name:  "maximum int64 cents convert without overflow",
			cents: math.MaxInt64, wantTicks: "9223372036854775807000000", wantError: false,
		},
		{
			name:  "negative cents are rejected",
			cents: -1, wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := accounting.FromCents(tt.cents)
			if tt.wantError {
				if err == nil {
					t.Fatalf("FromCents(%d) error = nil; want an error", tt.cents)
				}
				return
			}
			if err != nil {
				t.Fatalf("FromCents(%d) error = %v; want nil", tt.cents, err)
			}
			if got.Ticks().String() != tt.wantTicks {
				t.Errorf("FromCents(%d) = %s ticks; want %s ticks", tt.cents, got.Ticks(), tt.wantTicks)
			}
		})
	}
}

// UsageCharge converts units through the price denominator and money resolution
// exactly, even for one unit or large products. Free prices and zero units are
// valid; negative units and prices fail.
func TestUsageCharge(t *testing.T) {
	tests := []struct {
		name                 string
		units                int64
		pricePerMillionCents int64
		wantTicks            string
		wantError            bool
	}{
		{
			name:                 "one unit retains an exact sub-cent charge",
			units:                1,
			pricePerMillionCents: 5,
			wantTicks:            "5",
			wantError:            false,
		},
		{
			name:                 "small consumption retains fractional cents",
			units:                1_000,
			pricePerMillionCents: 5,
			wantTicks:            "5000",
			wantError:            false,
		},
		{
			name:                 "maximum units and price remain exact beyond int64",
			units:                math.MaxInt64,
			pricePerMillionCents: math.MaxInt64,
			wantTicks:            "85070591730234615847396907784232501249",
			wantError:            false,
		},
		{
			name:                 "zero units incur no charge",
			units:                0,
			pricePerMillionCents: 5,
			wantTicks:            "0",
			wantError:            false,
		},
		{
			name:                 "free price incurs no charge",
			units:                1,
			pricePerMillionCents: 0,
			wantTicks:            "0",
			wantError:            false,
		},
		{
			name:                 "negative units are rejected",
			units:                -1,
			pricePerMillionCents: 5,
			wantError:            true,
		},
		{
			name:                 "negative price is rejected",
			units:                1,
			pricePerMillionCents: -1,
			wantError:            true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := accounting.UsageCharge(tt.units, tt.pricePerMillionCents)
			if tt.wantError {
				if err == nil {
					t.Fatalf("UsageCharge(%d, %d) error = nil; want an error", tt.units, tt.pricePerMillionCents)
				}
				return
			}
			if err != nil {
				t.Fatalf("UsageCharge(%d, %d) error = %v; want nil", tt.units, tt.pricePerMillionCents, err)
			}
			if got.Ticks().String() != tt.wantTicks {
				t.Errorf("UsageCharge(%d, %d) = %s ticks; want %s ticks", tt.units, tt.pricePerMillionCents, got.Ticks(), tt.wantTicks)
			}
		})
	}
}

// Amount.Ticks returns the exact amount, including the zero value, and gives
// callers a copy whose mutation cannot alter the amount or its value copies.
func TestAmountTicks(t *testing.T) {
	tests := []struct {
		name          string
		amount        accounting.Amount
		mutateTicksTo int64
		wantTicks     string
	}{
		{
			name:          "zero value represents zero ticks",
			amount:        accounting.Amount{},
			mutateTicksTo: 1,
			wantTicks:     "0",
		},
		{
			name:          "returned integer is independent of the amount",
			amount:        ticks(t, "500000"),
			mutateTicksTo: 0,
			wantTicks:     "500000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			copy := tt.amount
			got := tt.amount.Ticks()
			if got.String() != tt.wantTicks {
				t.Errorf("Amount.Ticks() = %s; want %s", got, tt.wantTicks)
			}
			got.SetInt64(tt.mutateTicksTo)
			if original := tt.amount.Ticks().String(); original != tt.wantTicks {
				t.Errorf("Amount.Ticks() after returned integer changed to %d = %s; want %s", tt.mutateTicksTo, original, tt.wantTicks)
			}
			if copied := copy.Ticks().String(); copied != tt.wantTicks {
				t.Errorf("Copied amount after returned integer changed to %d = %s; want %s", tt.mutateTicksTo, copied, tt.wantTicks)
			}
		})
	}
}

// Amount.Add returns an exact sum without changing either operand, including
// value copies and sums that exceed the signed 64-bit range.
func TestAmountAdd(t *testing.T) {
	tests := []struct {
		name       string
		leftTicks  string
		rightTicks string
		wantTicks  string
	}{
		{
			name:      "zero plus zero remains zero",
			leftTicks: "0", rightTicks: "0", wantTicks: "0",
		},
		{
			name:      "two half cents make one cent",
			leftTicks: "500000", rightTicks: "500000", wantTicks: "1000000",
		},
		{
			name:      "sum can exceed int64 ticks",
			leftTicks: "9223372036854775807", rightTicks: "1", wantTicks: "9223372036854775808",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left, right := ticks(t, tt.leftTicks), ticks(t, tt.rightTicks)
			copy := left
			got := copy.Add(right)
			if got.Ticks().String() != tt.wantTicks {
				t.Errorf("Amount.Add(%s, %s) = %s ticks; want %s ticks", tt.leftTicks, tt.rightTicks, got.Ticks(), tt.wantTicks)
			}
			if left.Ticks().String() != tt.leftTicks || copy.Ticks().String() != tt.leftTicks {
				t.Errorf("Amount.Add changed its receiver or value copy; want %s ticks", tt.leftTicks)
			}
			if right.Ticks().String() != tt.rightTicks {
				t.Errorf("Amount.Add changed its argument to %s ticks; want %s ticks", right.Ticks(), tt.rightTicks)
			}
		})
	}
}

// Amount.Compare orders exact amounts and returns -1, 0, or 1 for smaller,
// equal, or larger receivers without rounding fractional cents.
func TestAmountCompare(t *testing.T) {
	tests := []struct {
		name       string
		leftTicks  string
		rightTicks string
		wantOrder  int
	}{
		{
			name:      "fractional cent below one cent is smaller",
			leftTicks: "999999", rightTicks: "1000000", wantOrder: -1,
		},
		{
			name:      "equal exact amounts compare equal",
			leftTicks: "500000", rightTicks: "500000", wantOrder: 0,
		},
		{
			name:      "amount above int64 remains greater",
			leftTicks: "9223372036854775808", rightTicks: "9223372036854775807", wantOrder: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ticks(t, tt.leftTicks).Compare(ticks(t, tt.rightTicks))
			if got != tt.wantOrder {
				t.Errorf("Amount.Compare(%s, %s) = %d; want %d", tt.leftTicks, tt.rightTicks, got, tt.wantOrder)
			}
		})
	}
}

// Amount.Subtract preserves both inputs and returns an exact non-negative
// difference; subtracting a larger amount is rejected.
func TestAmountSubtract(t *testing.T) {
	tests := []struct {
		name       string
		leftTicks  string
		rightTicks string
		wantTicks  string
		wantError  bool
	}{
		{
			name:      "subtracting a half cent retains the other half",
			leftTicks: "1000000", rightTicks: "500000", wantTicks: "500000", wantError: false,
		},
		{
			name:      "subtracting an equal amount leaves zero",
			leftTicks: "500000", rightTicks: "500000", wantTicks: "0", wantError: false,
		},
		{
			name:      "negative difference is rejected",
			leftTicks: "500000", rightTicks: "1000000", wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left, right := ticks(t, tt.leftTicks), ticks(t, tt.rightTicks)
			got, err := left.Subtract(right)
			if tt.wantError {
				if err == nil {
					t.Fatalf("Amount.Subtract(%s, %s) error = nil; want an error", tt.leftTicks, tt.rightTicks)
				}
				return
			}
			if err != nil {
				t.Fatalf("Amount.Subtract(%s, %s) error = %v; want nil", tt.leftTicks, tt.rightTicks, err)
			}
			if got.Ticks().String() != tt.wantTicks {
				t.Errorf("Amount.Subtract(%s, %s) = %s ticks; want %s ticks", tt.leftTicks, tt.rightTicks, got.Ticks(), tt.wantTicks)
			}
			if left.Ticks().String() != tt.leftTicks || right.Ticks().String() != tt.rightTicks {
				t.Errorf("Amount.Subtract changed its operands; want %s and %s ticks", tt.leftTicks, tt.rightTicks)
			}
		})
	}
}

// Amount.RoundCents rounds half cents upward, preserves representable maximum
// cents, and rejects a rounded result above the signed 64-bit range.
func TestAmountRoundCents(t *testing.T) {
	tests := []struct {
		name      string
		ticks     string
		wantCents int64
		wantError bool
	}{
		{
			name:  "zero remains zero cents",
			ticks: "0", wantCents: 0, wantError: false,
		},
		{
			name:  "small fractional charge rounds down",
			ticks: "5000", wantCents: 0, wantError: false,
		},
		{
			name:  "one tick below half a cent rounds down",
			ticks: "499999", wantCents: 0, wantError: false,
		},
		{
			name:  "exact half cent rounds up",
			ticks: "500000", wantCents: 1, wantError: false,
		},
		{
			name:  "one tick below a cent rounds to one cent",
			ticks: "999999", wantCents: 1, wantError: false,
		},
		{
			name:  "whole cent remains unchanged",
			ticks: "1000000", wantCents: 1, wantError: false,
		},
		{
			name:  "one and a half cents round to two cents",
			ticks: "1500000", wantCents: 2, wantError: false,
		},
		{
			name:  "assignment fractional amount rounds to whole cents",
			ticks: "617283945", wantCents: 617, wantError: false,
		},
		{
			name:  "maximum whole cents remain representable",
			ticks: "9223372036854775807000000", wantCents: math.MaxInt64, wantError: false,
		},
		{
			name:  "last tick before maximum cents overflow still rounds down",
			ticks: "9223372036854775807499999", wantCents: math.MaxInt64, wantError: false,
		},
		{
			name:  "half cent above maximum cents causes overflow",
			ticks: "9223372036854775807500000", wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ticks(t, tt.ticks).RoundCents()
			if tt.wantError {
				if err == nil {
					t.Fatalf("Amount.RoundCents(%s ticks) error = nil; want an error", tt.ticks)
				}
				return
			}
			if err != nil {
				t.Fatalf("Amount.RoundCents(%s ticks) error = %v; want nil", tt.ticks, err)
			}
			if got != tt.wantCents {
				t.Errorf("Amount.RoundCents(%s ticks) = %d cents; want %d cents", tt.ticks, got, tt.wantCents)
			}
		})
	}
}

// AllocateCredit consumes exact ticks before rounding. Repeated small charges
// match one combined charge, and later grants leave earlier allocations intact.
func TestAllocateCredit(t *testing.T) {
	tests := []struct {
		name               string
		availableTicks     string
		chargeTicks        []string
		repetitions        int
		wantUsedTicks      string
		wantRemainingTicks string
		wantNetChargeTicks string
	}{
		{
			name:               "small charge preserves fractional credit",
			availableTicks:     "1000000",
			chargeTicks:        []string{"5000"},
			repetitions:        1,
			wantUsedTicks:      "5000",
			wantRemainingTicks: "995000",
			wantNetChargeTicks: "0",
		},
		{
			name:               "charge without credit remains payable",
			availableTicks:     "0",
			chargeTicks:        []string{"1000000"},
			repetitions:        1,
			wantUsedTicks:      "0",
			wantRemainingTicks: "0",
			wantNetChargeTicks: "1000000",
		},
		{
			name:               "credit greater than charge leaves a balance",
			availableTicks:     "2000000",
			chargeTicks:        []string{"1000000"},
			repetitions:        1,
			wantUsedTicks:      "1000000",
			wantRemainingTicks: "1000000",
			wantNetChargeTicks: "0",
		},
		{
			name:               "one thousand small events exhaust only the available credit",
			availableTicks:     "1000000",
			chargeTicks:        []string{"5000"},
			repetitions:        1_000,
			wantUsedTicks:      "1000000",
			wantRemainingTicks: "0",
			wantNetChargeTicks: "4000000",
		},
		{
			name:               "one combined event has the same allocation as split events",
			availableTicks:     "1000000",
			chargeTicks:        []string{"5000000"},
			repetitions:        1,
			wantUsedTicks:      "1000000",
			wantRemainingTicks: "0",
			wantNetChargeTicks: "4000000",
		},
		{
			name:               "Acme October charges leave thirteen dollars of credit",
			availableTicks:     "2500000000",
			chargeTicks:        []string{"400000000", "800000000"},
			repetitions:        1,
			wantUsedTicks:      "1200000000",
			wantRemainingTicks: "1300000000",
			wantNetChargeTicks: "0",
		},
		{
			name:               "Acme November charges including late October usage leave seven dollars",
			availableTicks:     "1300000000",
			chargeTicks:        []string{"200000000", "400000000"},
			repetitions:        1,
			wantUsedTicks:      "600000000",
			wantRemainingTicks: "700000000",
			wantNetChargeTicks: "0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remaining := ticks(t, tt.availableTicks)
			used, net := new(big.Int), new(big.Int)
			for i := 0; i < tt.repetitions; i++ {
				for _, charge := range tt.chargeTicks {
					allocation := accounting.AllocateCredit(ticks(t, charge), remaining)
					remaining = allocation.Remaining
					used.Add(used, allocation.Used.Ticks())
					net.Add(net, allocation.NetCharge.Ticks())
				}
			}
			if used.String() != tt.wantUsedTicks {
				t.Errorf("AllocateCredit used = %s ticks; want %s ticks", used, tt.wantUsedTicks)
			}
			if remaining.Ticks().String() != tt.wantRemainingTicks {
				t.Errorf("AllocateCredit remaining = %s ticks; want %s ticks", remaining.Ticks(), tt.wantRemainingTicks)
			}
			if net.String() != tt.wantNetChargeTicks {
				t.Errorf("AllocateCredit net charge = %s ticks; want %s ticks", net, tt.wantNetChargeTicks)
			}
		})
	}

	t.Run("later credit grant leaves the earlier allocation unchanged", func(t *testing.T) {
		chargeTicks := "1000000"
		availableBeforeGrant := "0"
		availableAfterGrant := "2000000"
		want := []struct {
			usedTicks, remainingTicks, netChargeTicks string
		}{
			{usedTicks: "0", remainingTicks: "0", netChargeTicks: "1000000"},
			{usedTicks: "1000000", remainingTicks: "1000000", netChargeTicks: "0"},
		}

		first := accounting.AllocateCredit(ticks(t, chargeTicks), ticks(t, availableBeforeGrant))
		second := accounting.AllocateCredit(ticks(t, chargeTicks), ticks(t, availableAfterGrant))
		for i, got := range []accounting.CreditAllocation{first, second} {
			if got.Used.Ticks().String() != want[i].usedTicks ||
				got.Remaining.Ticks().String() != want[i].remainingTicks ||
				got.NetCharge.Ticks().String() != want[i].netChargeTicks {
				t.Errorf("Allocation %d = {used: %s, remaining: %s, net: %s} ticks; want %+v", i, got.Used.Ticks(), got.Remaining.Ticks(), got.NetCharge.Ticks(), want[i])
			}
		}
	})
}

// InvoiceAmounts rounds gross and net group totals and derives balanced credit
// lines; assignment amounts remain exact and over-allocation or overflow fails.
func TestInvoiceAmounts(t *testing.T) {
	tests := []struct {
		name        string
		grossTicks  string
		creditTicks string
		want        accounting.InvoiceCharge
		wantError   bool
	}{
		{
			name:        "half-cent credit does not independently round the credit line",
			grossTicks:  "1000000",
			creditTicks: "500000",
			want:        accounting.InvoiceCharge{GrossCents: 1, CreditCents: 0, NetCents: 1},
			wantError:   false,
		},
		{
			name:        "rounded gross and net derive a one-cent credit line",
			grossTicks:  "1500000",
			creditTicks: "500000",
			want:        accounting.InvoiceCharge{GrossCents: 2, CreditCents: 1, NetCents: 1},
			wantError:   false,
		},
		{
			name:        "fully credited sub-cent charge presents zero cents",
			grossTicks:  "5000",
			creditTicks: "5000",
			want:        accounting.InvoiceCharge{GrossCents: 0, CreditCents: 0, NetCents: 0},
			wantError:   false,
		},
		{
			name:        "whole-cent group displays its gross credit and net",
			grossTicks:  "1000000000",
			creditTicks: "700000000",
			want:        accounting.InvoiceCharge{GrossCents: 1_000, CreditCents: 700, NetCents: 300},
			wantError:   false,
		},
		{
			name:        "split-event group owes four cents after one cent of credit",
			grossTicks:  "5000000",
			creditTicks: "1000000",
			want:        accounting.InvoiceCharge{GrossCents: 5, CreditCents: 1, NetCents: 4},
			wantError:   false,
		},
		{
			name:        "Acme October consumption is fully credited",
			grossTicks:  "1200000000",
			creditTicks: "1200000000",
			want:        accounting.InvoiceCharge{GrossCents: 1_200, CreditCents: 1_200, NetCents: 0},
			wantError:   false,
		},
		{
			name:        "Acme November consumption including late usage is fully credited",
			grossTicks:  "600000000",
			creditTicks: "600000000",
			want:        accounting.InvoiceCharge{GrossCents: 600, CreditCents: 600, NetCents: 0},
			wantError:   false,
		},
		{
			name:        "Cyberdyne first price group rounds to six dollars seventeen cents",
			grossTicks:  "617283945",
			creditTicks: "0",
			want:        accounting.InvoiceCharge{GrossCents: 617, CreditCents: 0, NetCents: 617},
			wantError:   false,
		},
		{
			name:        "Cyberdyne second price group owes twelve dollars",
			grossTicks:  "1200000000",
			creditTicks: "0",
			want:        accounting.InvoiceCharge{GrossCents: 1_200, CreditCents: 0, NetCents: 1_200},
			wantError:   false,
		},
		{
			name:        "credit exceeding gross is rejected",
			grossTicks:  "1",
			creditTicks: "2",
			wantError:   true,
		},
		{
			name:        "rounded gross exceeding int64 cents is rejected",
			grossTicks:  "9223372036854775807500000",
			creditTicks: "0",
			wantError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := accounting.InvoiceAmounts(ticks(t, tt.grossTicks), ticks(t, tt.creditTicks))
			if tt.wantError {
				if err == nil {
					t.Fatalf("InvoiceAmounts(%s, %s ticks) error = nil; want an error", tt.grossTicks, tt.creditTicks)
				}
				return
			}
			if err != nil {
				t.Fatalf("InvoiceAmounts(%s, %s ticks) error = %v; want nil", tt.grossTicks, tt.creditTicks, err)
			}
			if got != tt.want {
				t.Errorf("InvoiceAmounts(%s, %s ticks) = %+v; want %+v", tt.grossTicks, tt.creditTicks, got, tt.want)
			}
			if got.GrossCents-got.CreditCents != got.NetCents {
				t.Errorf("InvoiceAmounts lines do not balance: %+v; want gross - credit = net", got)
			}
		})
	}

	// Keep the assignment invoice totals while testing only InvoiceAmounts:
	// rating, credit allocation, and the add-on price are explicit input fixtures.
	type invoiceGroup struct {
		name        string
		grossTicks  string
		creditTicks string
	}
	invoices := []struct {
		name         string
		groups       []invoiceGroup
		wantNetCents int64
	}{
		{
			name: "Acme October invoice totals twenty dollars including the add-on",
			groups: []invoiceGroup{
				{name: "usage", grossTicks: "1200000000", creditTicks: "1200000000"},
				{name: "credit-excluded add-on", grossTicks: "2000000000", creditTicks: "0"},
			},
			wantNetCents: 2_000,
		},
		{
			name: "Acme November invoice totals twenty dollars including late usage",
			groups: []invoiceGroup{
				{name: "usage including late October", grossTicks: "600000000", creditTicks: "600000000"},
				{name: "credit-excluded add-on", grossTicks: "2000000000", creditTicks: "0"},
			},
			wantNetCents: 2_000,
		},
		{
			name: "Cyberdyne invoice totals eighteen dollars seventeen cents",
			groups: []invoiceGroup{
				{name: "old default price", grossTicks: "617283945", creditTicks: "0"},
				{name: "new default price", grossTicks: "1200000000", creditTicks: "0"},
			},
			wantNetCents: 1_817,
		},
	}
	for _, tt := range invoices {
		t.Run(tt.name, func(t *testing.T) {
			var gotNetCents int64
			for _, group := range tt.groups {
				got, err := accounting.InvoiceAmounts(ticks(t, group.grossTicks), ticks(t, group.creditTicks))
				if err != nil {
					t.Fatalf("InvoiceAmounts for %s error = %v; want nil", group.name, err)
				}
				gotNetCents += got.NetCents
			}
			if gotNetCents != tt.wantNetCents {
				t.Errorf("InvoiceAmounts net total = %d cents; want %d cents", gotNetCents, tt.wantNetCents)
			}
		})
	}
}

// LimitReached compares exact gross ticks before rounding; nil is unlimited,
// zero is immediately reached, and negative whole-cent limits are rejected.
func TestLimitReached(t *testing.T) {
	tests := []struct {
		name        string
		grossTicks  string
		limitCents  *int64
		wantReached bool
		wantError   bool
	}{
		{
			name:        "zero limit is reached without usage",
			grossTicks:  "0",
			limitCents:  centLimit(0),
			wantReached: true,
			wantError:   false,
		},
		{
			name:        "one tick below the limit remains below despite cent rounding",
			grossTicks:  "999999",
			limitCents:  centLimit(1),
			wantReached: false,
			wantError:   false,
		},
		{
			name:        "exactly the limit is reached",
			grossTicks:  "1000000",
			limitCents:  centLimit(1),
			wantReached: true,
			wantError:   false,
		},
		{
			name:        "Cyberdyne first charge is below fifteen dollars",
			grossTicks:  "617283945",
			limitCents:  centLimit(1_500),
			wantReached: false,
			wantError:   false,
		},
		{
			name:        "Cyberdyne combined gross exceeds fifteen dollars",
			grossTicks:  "1817283945",
			limitCents:  centLimit(1_500),
			wantReached: true,
			wantError:   false,
		},
		{
			name:        "absent limit permits unlimited usage",
			grossTicks:  "1000000",
			limitCents:  nil,
			wantReached: false,
			wantError:   false,
		},
		{
			name:       "negative limit is rejected",
			grossTicks: "0",
			limitCents: centLimit(-1),
			wantError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := accounting.LimitReached(ticks(t, tt.grossTicks), tt.limitCents)
			if tt.wantError {
				if err == nil {
					t.Fatalf("LimitReached(%s ticks) error = nil; want an error", tt.grossTicks)
				}
				return
			}
			if err != nil {
				t.Fatalf("LimitReached(%s ticks) error = %v; want nil", tt.grossTicks, err)
			}
			if got != tt.wantReached {
				t.Errorf("LimitReached(%s ticks) = %v; want %v", tt.grossTicks, got, tt.wantReached)
			}
		})
	}
}

// Parse literal tick fixtures without floating-point conversion or an unchecked
// constructor; decimal strings also represent exact amounts beyond int64.
func ticks(t *testing.T, value string) accounting.Amount {
	t.Helper()
	integer, ok := new(big.Int).SetString(value, 10)
	if !ok {
		t.Fatalf("Invalid tick fixture: %s", value)
	}
	amount, err := accounting.FromTicks(integer)
	if err != nil {
		t.Fatalf("Invalid tick fixture %s: %v", value, err)
	}
	return amount
}

// Give a literal whole-cent limit its optional pointer representation so nil
// and explicit zero remain visibly different inputs in LimitReached cases.
func centLimit(value int64) *int64 {
	return &value
}
