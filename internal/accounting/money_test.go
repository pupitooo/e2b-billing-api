package accounting_test

import (
	"math"
	"math/big"
	"testing"

	"e2b/billing-api/internal/accounting"
)

// FromMicrocents accepts non-negative integers, rejects nil and negative values, and
// copies caller-owned input so later mutations cannot change the returned amount.
func TestFromMicrocents(t *testing.T) {
	tests := []struct {
		name           string
		input          *big.Int
		wantMicrocents string
		wantError      bool
	}{
		{
			name:  "zero microcents are valid",
			input: big.NewInt(0), wantMicrocents: "0", wantError: false,
		},
		{
			name:  "fractional cent microcents remain exact",
			input: big.NewInt(500_000), wantMicrocents: "500000", wantError: false,
		},
		{
			name:  "missing integer is rejected",
			input: nil, wantError: true,
		},
		{
			name:  "negative microcents are rejected",
			input: big.NewInt(-1), wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := accounting.FromMicrocents(tt.input)
			if tt.wantError {
				if err == nil {
					t.Fatalf("FromMicrocents(%v) error = nil; want an error", tt.input)
				}

				return
			}

			if err != nil {
				t.Fatalf("FromMicrocents(%v) error = %v; want nil", tt.input, err)
			}

			if got.Microcents().String() != tt.wantMicrocents {
				t.Errorf("FromMicrocents(%v) = %s microcents; want %s microcents", tt.input, got.Microcents(), tt.wantMicrocents)
			}
		})
	}

	t.Run("changing the caller's integer preserves the constructed amount", func(t *testing.T) {
		input := big.NewInt(500_000)
		mutateInputTo := int64(0)
		wantMicrocents := "500000"

		got, err := accounting.FromMicrocents(input)
		if err != nil {
			t.Fatalf("FromMicrocents(%s) error = %v; want nil", input, err)
		}

		input.SetInt64(mutateInputTo)
		if got.Microcents().String() != wantMicrocents {
			t.Errorf("FromMicrocents result after input changed to %d = %s microcents; want %s microcents", mutateInputTo, got.Microcents(), wantMicrocents)
		}
	})
}

// FromCents converts whole cents to exact microcents without int64 multiplication
// overflow; zero is valid and negative cents are rejected.
func TestFromCents(t *testing.T) {
	tests := []struct {
		name           string
		cents          int64
		wantMicrocents string
		wantError      bool
	}{
		{
			name:  "zero cents remain zero",
			cents: 0, wantMicrocents: "0", wantError: false,
		},
		{
			name:  "one cent contains one million microcents",
			cents: 1, wantMicrocents: "1000000", wantError: false,
		},
		{
			name:  "maximum int64 cents convert without overflow",
			cents: math.MaxInt64, wantMicrocents: "9223372036854775807000000", wantError: false,
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

			if got.Microcents().String() != tt.wantMicrocents {
				t.Errorf("FromCents(%d) = %s microcents; want %s microcents", tt.cents, got.Microcents(), tt.wantMicrocents)
			}
		})
	}
}

// UsageCharge multiplies units by cents per million to produce exact microcents,
// even for one unit or large products. Free prices and zero units are
// valid; negative units and prices fail.
func TestUsageCharge(t *testing.T) {
	tests := []struct {
		name                 string
		units                int64
		pricePerMillionCents int64
		wantMicrocents       string
		wantError            bool
	}{
		{
			name:                 "one unit retains an exact sub-cent charge",
			units:                1,
			pricePerMillionCents: 5,
			wantMicrocents:       "5",
			wantError:            false,
		},
		{
			name:                 "small consumption retains fractional cents",
			units:                1_000,
			pricePerMillionCents: 5,
			wantMicrocents:       "5000",
			wantError:            false,
		},
		{
			name:                 "maximum units and price remain exact beyond int64",
			units:                math.MaxInt64,
			pricePerMillionCents: math.MaxInt64,
			wantMicrocents:       "85070591730234615847396907784232501249",
			wantError:            false,
		},
		{
			name:                 "zero units incur no charge",
			units:                0,
			pricePerMillionCents: 5,
			wantMicrocents:       "0",
			wantError:            false,
		},
		{
			name:                 "free price incurs no charge",
			units:                1,
			pricePerMillionCents: 0,
			wantMicrocents:       "0",
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

			if got.Microcents().String() != tt.wantMicrocents {
				t.Errorf("UsageCharge(%d, %d) = %s microcents; want %s microcents", tt.units, tt.pricePerMillionCents, got.Microcents(), tt.wantMicrocents)
			}
		})
	}
}

// Amount.Microcents returns the exact amount, including the zero value, and gives
// callers a copy whose mutation cannot alter the amount or its value copies.
func TestAmountMicrocents(t *testing.T) {
	tests := []struct {
		name               string
		amount             accounting.Amount
		mutateMicrocentsTo int64
		wantMicrocents     string
	}{
		{
			name:               "zero value represents zero microcents",
			amount:             accounting.Amount{},
			mutateMicrocentsTo: 1,
			wantMicrocents:     "0",
		},
		{
			name:   "overwriting a nonzero word cannot change the amount or its value copy",
			amount: microcents(t, "500000"), mutateMicrocentsTo: 1, wantMicrocents: "500000",
		},
		{
			name:               "returned integer is independent of the amount",
			amount:             microcents(t, "500000"),
			mutateMicrocentsTo: 0,
			wantMicrocents:     "500000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			copy := tt.amount
			got := tt.amount.Microcents()
			if got.String() != tt.wantMicrocents {
				t.Errorf("Amount.Microcents() = %s; want %s", got, tt.wantMicrocents)
			}

			got.SetInt64(tt.mutateMicrocentsTo)
			if original := tt.amount.Microcents().String(); original != tt.wantMicrocents {
				t.Errorf("Amount.Microcents() after returned integer changed to %d = %s; want %s", tt.mutateMicrocentsTo, original, tt.wantMicrocents)
			}

			if copied := copy.Microcents().String(); copied != tt.wantMicrocents {
				t.Errorf("Copied amount after returned integer changed to %d = %s; want %s", tt.mutateMicrocentsTo, copied, tt.wantMicrocents)
			}
		})
	}
}

// Amount.Add returns an exact sum without changing either operand, including
// value copies and sums that exceed the signed 64-bit range.
func TestAmountAdd(t *testing.T) {
	tests := []struct {
		name            string
		leftMicrocents  string
		rightMicrocents string
		wantMicrocents  string
	}{
		{
			name:           "zero plus zero remains zero",
			leftMicrocents: "0", rightMicrocents: "0", wantMicrocents: "0",
		},
		{
			name:           "two half cents make one cent",
			leftMicrocents: "500000", rightMicrocents: "500000", wantMicrocents: "1000000",
		},
		{
			name:           "sum can exceed int64 microcents",
			leftMicrocents: "9223372036854775807", rightMicrocents: "1", wantMicrocents: "9223372036854775808",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left, right := microcents(t, tt.leftMicrocents), microcents(t, tt.rightMicrocents)
			copy := left
			got := copy.Add(right)
			if got.Microcents().String() != tt.wantMicrocents {
				t.Errorf("Amount.Add(%s, %s) = %s microcents; want %s microcents", tt.leftMicrocents, tt.rightMicrocents, got.Microcents(), tt.wantMicrocents)
			}

			if left.Microcents().String() != tt.leftMicrocents || copy.Microcents().String() != tt.leftMicrocents {
				t.Errorf("Amount.Add changed its receiver or value copy; want %s microcents", tt.leftMicrocents)
			}

			if right.Microcents().String() != tt.rightMicrocents {
				t.Errorf("Amount.Add changed its argument to %s microcents; want %s microcents", right.Microcents(), tt.rightMicrocents)
			}
		})
	}
}

// Amount.Compare orders exact amounts and returns -1, 0, or 1 for smaller,
// equal, or larger receivers without rounding fractional cents.
func TestAmountCompare(t *testing.T) {
	tests := []struct {
		name            string
		leftMicrocents  string
		rightMicrocents string
		wantOrder       int
	}{
		{
			name:           "fractional cent below one cent is smaller",
			leftMicrocents: "999999", rightMicrocents: "1000000", wantOrder: -1,
		},
		{
			name:           "equal exact amounts compare equal",
			leftMicrocents: "500000", rightMicrocents: "500000", wantOrder: 0,
		},
		{
			name:           "amount above int64 remains greater",
			leftMicrocents: "9223372036854775808", rightMicrocents: "9223372036854775807", wantOrder: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := microcents(t, tt.leftMicrocents).Compare(microcents(t, tt.rightMicrocents))
			if got != tt.wantOrder {
				t.Errorf("Amount.Compare(%s, %s) = %d; want %d", tt.leftMicrocents, tt.rightMicrocents, got, tt.wantOrder)
			}
		})
	}
}

// Amount.Subtract preserves both inputs and returns an exact non-negative
// difference; subtracting a larger amount is rejected.
func TestAmountSubtract(t *testing.T) {
	tests := []struct {
		name            string
		leftMicrocents  string
		rightMicrocents string
		wantMicrocents  string
		wantError       bool
	}{
		{
			name:           "subtracting a half cent retains the other half",
			leftMicrocents: "1000000", rightMicrocents: "500000", wantMicrocents: "500000", wantError: false,
		},
		{
			name:           "subtracting an equal amount leaves zero",
			leftMicrocents: "500000", rightMicrocents: "500000", wantMicrocents: "0", wantError: false,
		},
		{
			name:           "negative difference is rejected",
			leftMicrocents: "500000", rightMicrocents: "1000000", wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left, right := microcents(t, tt.leftMicrocents), microcents(t, tt.rightMicrocents)
			got, err := left.Subtract(right)
			if tt.wantError {
				if err == nil {
					t.Fatalf("Amount.Subtract(%s, %s) error = nil; want an error", tt.leftMicrocents, tt.rightMicrocents)
				}

				return
			}

			if err != nil {
				t.Fatalf("Amount.Subtract(%s, %s) error = %v; want nil", tt.leftMicrocents, tt.rightMicrocents, err)
			}

			if got.Microcents().String() != tt.wantMicrocents {
				t.Errorf("Amount.Subtract(%s, %s) = %s microcents; want %s microcents", tt.leftMicrocents, tt.rightMicrocents, got.Microcents(), tt.wantMicrocents)
			}

			if left.Microcents().String() != tt.leftMicrocents || right.Microcents().String() != tt.rightMicrocents {
				t.Errorf("Amount.Subtract changed its operands; want %s and %s microcents", tt.leftMicrocents, tt.rightMicrocents)
			}
		})
	}
}

// Amount.RoundCents rounds half cents upward, preserves representable maximum
// cents, and rejects a rounded result above the signed 64-bit range.
func TestAmountRoundCents(t *testing.T) {
	tests := []struct {
		name       string
		microcents string
		wantCents  int64
		wantError  bool
	}{
		{
			name:       "zero remains zero cents",
			microcents: "0", wantCents: 0, wantError: false,
		},
		{
			name:       "small fractional charge rounds down",
			microcents: "5000", wantCents: 0, wantError: false,
		},
		{
			name:       "one microcent below half a cent rounds down",
			microcents: "499999", wantCents: 0, wantError: false,
		},
		{
			name:       "exact half cent rounds up",
			microcents: "500000", wantCents: 1, wantError: false,
		},
		{
			name:       "one microcent below a cent rounds to one cent",
			microcents: "999999", wantCents: 1, wantError: false,
		},
		{
			name:       "whole cent remains unchanged",
			microcents: "1000000", wantCents: 1, wantError: false,
		},
		{
			name:       "one and a half cents round to two cents",
			microcents: "1500000", wantCents: 2, wantError: false,
		},
		{
			name:       "assignment fractional amount rounds to whole cents",
			microcents: "617283945", wantCents: 617, wantError: false,
		},
		{
			name:       "maximum whole cents remain representable",
			microcents: "9223372036854775807000000", wantCents: math.MaxInt64, wantError: false,
		},
		{
			name:       "last microcent before maximum cents overflow still rounds down",
			microcents: "9223372036854775807499999", wantCents: math.MaxInt64, wantError: false,
		},
		{
			name:       "half cent above maximum cents causes overflow",
			microcents: "9223372036854775807500000", wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := microcents(t, tt.microcents).RoundCents()
			if tt.wantError {
				if err == nil {
					t.Fatalf("Amount.RoundCents(%s microcents) error = nil; want an error", tt.microcents)
				}

				return
			}

			if err != nil {
				t.Fatalf("Amount.RoundCents(%s microcents) error = %v; want nil", tt.microcents, err)
			}

			if got != tt.wantCents {
				t.Errorf("Amount.RoundCents(%s microcents) = %d cents; want %d cents", tt.microcents, got, tt.wantCents)
			}
		})
	}
}

// AllocateCredit consumes exact microcents before rounding. Repeated small charges
// match one combined charge, and later grants leave earlier allocations intact.
func TestAllocateCredit(t *testing.T) {
	tests := []struct {
		name                    string
		availableMicrocents     string
		chargeMicrocents        []string
		repetitions             int
		wantUsedMicrocents      string
		wantRemainingMicrocents string
		wantNetChargeMicrocents string
	}{
		{
			name:                    "small charge preserves fractional credit",
			availableMicrocents:     "1000000",
			chargeMicrocents:        []string{"5000"},
			repetitions:             1,
			wantUsedMicrocents:      "5000",
			wantRemainingMicrocents: "995000",
			wantNetChargeMicrocents: "0",
		},
		{
			name:                    "charge without credit remains payable",
			availableMicrocents:     "0",
			chargeMicrocents:        []string{"1000000"},
			repetitions:             1,
			wantUsedMicrocents:      "0",
			wantRemainingMicrocents: "0",
			wantNetChargeMicrocents: "1000000",
		},
		{
			name:                    "credit greater than charge leaves a balance",
			availableMicrocents:     "2000000",
			chargeMicrocents:        []string{"1000000"},
			repetitions:             1,
			wantUsedMicrocents:      "1000000",
			wantRemainingMicrocents: "1000000",
			wantNetChargeMicrocents: "0",
		},
		{
			name:                    "one thousand small events exhaust only the available credit",
			availableMicrocents:     "1000000",
			chargeMicrocents:        []string{"5000"},
			repetitions:             1_000,
			wantUsedMicrocents:      "1000000",
			wantRemainingMicrocents: "0",
			wantNetChargeMicrocents: "4000000",
		},
		{
			name:                    "one combined event has the same allocation as split events",
			availableMicrocents:     "1000000",
			chargeMicrocents:        []string{"5000000"},
			repetitions:             1,
			wantUsedMicrocents:      "1000000",
			wantRemainingMicrocents: "0",
			wantNetChargeMicrocents: "4000000",
		},
		{
			name:                    "Acme October charges leave thirteen dollars of credit",
			availableMicrocents:     "2500000000",
			chargeMicrocents:        []string{"400000000", "800000000"},
			repetitions:             1,
			wantUsedMicrocents:      "1200000000",
			wantRemainingMicrocents: "1300000000",
			wantNetChargeMicrocents: "0",
		},
		{
			name:                    "Acme November charges including late October usage leave seven dollars",
			availableMicrocents:     "1300000000",
			chargeMicrocents:        []string{"200000000", "400000000"},
			repetitions:             1,
			wantUsedMicrocents:      "600000000",
			wantRemainingMicrocents: "700000000",
			wantNetChargeMicrocents: "0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remaining := microcents(t, tt.availableMicrocents)
			used, net := new(big.Int), new(big.Int)
			for i := 0; i < tt.repetitions; i++ {
				for _, charge := range tt.chargeMicrocents {
					allocation := accounting.AllocateCredit(microcents(t, charge), remaining)
					remaining = allocation.Remaining
					used.Add(used, allocation.Used.Microcents())
					net.Add(net, allocation.NetCharge.Microcents())
				}
			}

			if used.String() != tt.wantUsedMicrocents {
				t.Errorf("AllocateCredit used = %s microcents; want %s microcents", used, tt.wantUsedMicrocents)
			}

			if remaining.Microcents().String() != tt.wantRemainingMicrocents {
				t.Errorf("AllocateCredit remaining = %s microcents; want %s microcents", remaining.Microcents(), tt.wantRemainingMicrocents)
			}

			if net.String() != tt.wantNetChargeMicrocents {
				t.Errorf("AllocateCredit net charge = %s microcents; want %s microcents", net, tt.wantNetChargeMicrocents)
			}
		})
	}

	t.Run("later credit grant leaves the earlier allocation unchanged", func(t *testing.T) {
		chargeMicrocents := "1000000"
		availableBeforeGrant := "0"
		availableAfterGrant := "2000000"
		want := []struct {
			usedMicrocents, remainingMicrocents, netChargeMicrocents string
		}{
			{usedMicrocents: "0", remainingMicrocents: "0", netChargeMicrocents: "1000000"},
			{usedMicrocents: "1000000", remainingMicrocents: "1000000", netChargeMicrocents: "0"},
		}

		first := accounting.AllocateCredit(microcents(t, chargeMicrocents), microcents(t, availableBeforeGrant))
		second := accounting.AllocateCredit(microcents(t, chargeMicrocents), microcents(t, availableAfterGrant))
		for i, got := range []accounting.CreditAllocation{first, second} {
			if got.Used.Microcents().String() != want[i].usedMicrocents ||
				got.Remaining.Microcents().String() != want[i].remainingMicrocents ||
				got.NetCharge.Microcents().String() != want[i].netChargeMicrocents {
				t.Errorf("Allocation %d = {used: %s, remaining: %s, net: %s} microcents; want %+v", i, got.Used.Microcents(), got.Remaining.Microcents(), got.NetCharge.Microcents(), want[i])
			}
		}
	})
}

// InvoiceAmounts rounds gross and net group totals and derives balanced credit
// lines; assignment amounts remain exact and over-allocation or overflow fails.
// Positive uncredited amounts below half a cent are valid zero-cent charges;
// an exact half cent rounds up to one cent.
func TestInvoiceAmounts(t *testing.T) {
	tests := []struct {
		name             string
		grossMicrocents  string
		creditMicrocents string
		want             accounting.InvoiceCharge
		wantError        bool
	}{
		{
			name:             "uncredited one tenth cent remains a valid zero-cent charge",
			grossMicrocents:  "100000",
			creditMicrocents: "0",
			want:             accounting.InvoiceCharge{GrossCents: 0, CreditCents: 0, NetCents: 0},
			wantError:        false,
		},
		{
			name:             "uncredited charge one microcent below half a cent rounds to zero",
			grossMicrocents:  "499999",
			creditMicrocents: "0",
			want:             accounting.InvoiceCharge{GrossCents: 0, CreditCents: 0, NetCents: 0},
			wantError:        false,
		},
		{
			name:             "uncredited exact half cent rounds to one cent",
			grossMicrocents:  "500000",
			creditMicrocents: "0",
			want:             accounting.InvoiceCharge{GrossCents: 1, CreditCents: 0, NetCents: 1},
			wantError:        false,
		},
		{
			name:             "half-cent credit does not independently round the credit line",
			grossMicrocents:  "1000000",
			creditMicrocents: "500000",
			want:             accounting.InvoiceCharge{GrossCents: 1, CreditCents: 0, NetCents: 1},
			wantError:        false,
		},
		{
			name:             "rounded gross and net derive a one-cent credit line",
			grossMicrocents:  "1500000",
			creditMicrocents: "500000",
			want:             accounting.InvoiceCharge{GrossCents: 2, CreditCents: 1, NetCents: 1},
			wantError:        false,
		},
		{
			name:             "fully credited sub-cent charge presents zero cents",
			grossMicrocents:  "5000",
			creditMicrocents: "5000",
			want:             accounting.InvoiceCharge{GrossCents: 0, CreditCents: 0, NetCents: 0},
			wantError:        false,
		},
		{
			name:             "whole-cent group displays its gross credit and net",
			grossMicrocents:  "1000000000",
			creditMicrocents: "700000000",
			want:             accounting.InvoiceCharge{GrossCents: 1_000, CreditCents: 700, NetCents: 300},
			wantError:        false,
		},
		{
			name:             "split-event group owes four cents after one cent of credit",
			grossMicrocents:  "5000000",
			creditMicrocents: "1000000",
			want:             accounting.InvoiceCharge{GrossCents: 5, CreditCents: 1, NetCents: 4},
			wantError:        false,
		},
		{
			name:             "Acme October consumption is fully credited",
			grossMicrocents:  "1200000000",
			creditMicrocents: "1200000000",
			want:             accounting.InvoiceCharge{GrossCents: 1_200, CreditCents: 1_200, NetCents: 0},
			wantError:        false,
		},
		{
			name:             "Acme November consumption including late usage is fully credited",
			grossMicrocents:  "600000000",
			creditMicrocents: "600000000",
			want:             accounting.InvoiceCharge{GrossCents: 600, CreditCents: 600, NetCents: 0},
			wantError:        false,
		},
		{
			name:             "Cyberdyne first price group rounds to six dollars seventeen cents",
			grossMicrocents:  "617283945",
			creditMicrocents: "0",
			want:             accounting.InvoiceCharge{GrossCents: 617, CreditCents: 0, NetCents: 617},
			wantError:        false,
		},
		{
			name:             "Cyberdyne second price group owes twelve dollars",
			grossMicrocents:  "1200000000",
			creditMicrocents: "0",
			want:             accounting.InvoiceCharge{GrossCents: 1_200, CreditCents: 0, NetCents: 1_200},
			wantError:        false,
		},
		{
			name:             "credit exceeding gross is rejected",
			grossMicrocents:  "1",
			creditMicrocents: "2",
			wantError:        true,
		},
		{
			name:             "rounded gross exceeding int64 cents is rejected",
			grossMicrocents:  "9223372036854775807500000",
			creditMicrocents: "0",
			wantError:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := accounting.InvoiceAmounts(microcents(t, tt.grossMicrocents), microcents(t, tt.creditMicrocents))
			if tt.wantError {
				if err == nil {
					t.Fatalf("InvoiceAmounts(%s, %s microcents) error = nil; want an error", tt.grossMicrocents, tt.creditMicrocents)
				}

				return
			}

			if err != nil {
				t.Fatalf("InvoiceAmounts(%s, %s microcents) error = %v; want nil", tt.grossMicrocents, tt.creditMicrocents, err)
			}

			if got != tt.want {
				t.Errorf("InvoiceAmounts(%s, %s microcents) = %+v; want %+v", tt.grossMicrocents, tt.creditMicrocents, got, tt.want)
			}

			if got.GrossCents-got.CreditCents != got.NetCents {
				t.Errorf("InvoiceAmounts lines do not balance: %+v; want gross - credit = net", got)
			}
		})
	}

	// Keep the assignment invoice totals while testing only InvoiceAmounts:
	// rating, credit allocation, and the add-on price are explicit input fixtures.
	type invoiceGroup struct {
		name             string
		grossMicrocents  string
		creditMicrocents string
	}
	invoices := []struct {
		name         string
		groups       []invoiceGroup
		wantNetCents int64
	}{
		{
			name: "Acme October invoice totals twenty dollars including the add-on",
			groups: []invoiceGroup{
				{name: "usage", grossMicrocents: "1200000000", creditMicrocents: "1200000000"},
				{name: "credit-excluded add-on", grossMicrocents: "2000000000", creditMicrocents: "0"},
			},
			wantNetCents: 2_000,
		},
		{
			name: "Acme November invoice totals twenty dollars including late usage",
			groups: []invoiceGroup{
				{name: "usage including late October", grossMicrocents: "600000000", creditMicrocents: "600000000"},
				{name: "credit-excluded add-on", grossMicrocents: "2000000000", creditMicrocents: "0"},
			},
			wantNetCents: 2_000,
		},
		{
			name: "Cyberdyne invoice totals eighteen dollars seventeen cents",
			groups: []invoiceGroup{
				{name: "old default price", grossMicrocents: "617283945", creditMicrocents: "0"},
				{name: "new default price", grossMicrocents: "1200000000", creditMicrocents: "0"},
			},
			wantNetCents: 1_817,
		},
	}
	for _, tt := range invoices {
		t.Run(tt.name, func(t *testing.T) {
			var gotNetCents int64
			for _, group := range tt.groups {
				got, err := accounting.InvoiceAmounts(microcents(t, group.grossMicrocents), microcents(t, group.creditMicrocents))
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

// LimitReached compares exact gross microcents before rounding; nil is unlimited,
// zero is immediately reached, and negative whole-cent limits are rejected.
func TestLimitReached(t *testing.T) {
	tests := []struct {
		name            string
		grossMicrocents string
		limitCents      *int64
		wantReached     bool
		wantError       bool
	}{
		{
			name:            "zero limit is reached without usage",
			grossMicrocents: "0",
			limitCents:      centLimit(0),
			wantReached:     true,
			wantError:       false,
		},
		{
			name:            "one microcent below the limit remains below despite cent rounding",
			grossMicrocents: "999999",
			limitCents:      centLimit(1),
			wantReached:     false,
			wantError:       false,
		},
		{
			name:            "exactly the limit is reached",
			grossMicrocents: "1000000",
			limitCents:      centLimit(1),
			wantReached:     true,
			wantError:       false,
		},
		{
			name:            "Cyberdyne first charge is below fifteen dollars",
			grossMicrocents: "617283945",
			limitCents:      centLimit(1_500),
			wantReached:     false,
			wantError:       false,
		},
		{
			name:            "Cyberdyne combined gross exceeds fifteen dollars",
			grossMicrocents: "1817283945",
			limitCents:      centLimit(1_500),
			wantReached:     true,
			wantError:       false,
		},
		{
			name:            "absent limit permits unlimited usage",
			grossMicrocents: "1000000",
			limitCents:      nil,
			wantReached:     false,
			wantError:       false,
		},
		{
			name:            "negative limit is rejected",
			grossMicrocents: "0",
			limitCents:      centLimit(-1),
			wantError:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := accounting.LimitReached(microcents(t, tt.grossMicrocents), tt.limitCents)
			if tt.wantError {
				if err == nil {
					t.Fatalf("LimitReached(%s microcents) error = nil; want an error", tt.grossMicrocents)
				}

				return
			}

			if err != nil {
				t.Fatalf("LimitReached(%s microcents) error = %v; want nil", tt.grossMicrocents, err)
			}

			if got != tt.wantReached {
				t.Errorf("LimitReached(%s microcents) = %v; want %v", tt.grossMicrocents, got, tt.wantReached)
			}
		})
	}
}

// Parse literal microcent fixtures without floating-point conversion or an unchecked
// constructor; decimal strings also represent exact amounts beyond int64.
func microcents(t *testing.T, value string) accounting.Amount {
	t.Helper()
	integer, ok := new(big.Int).SetString(value, 10)
	if !ok {
		t.Fatalf("Invalid microcent fixture: %s", value)
	}

	amount, err := accounting.FromMicrocents(integer)
	if err != nil {
		t.Fatalf("Invalid microcent fixture %s: %v", value, err)
	}

	return amount
}

// Give a literal whole-cent limit its optional pointer representation so nil
// and explicit zero remain visibly different inputs in LimitReached cases.
func centLimit(value int64) *int64 {
	return &value
}
