package billing

import (
	"math"
	"math/big"
	"reflect"
	"testing"
	"time"

	"e2b/billing-api/internal/accounting"
)

type invoiceUsageFixture struct {
	priceVersionID string
	metric         string
	usageMonth     string
	units          string
	grossTicks     string
	creditTicks    string
}

// TestAssembleInvoice verifies financial presentation without PostgreSQL: empty
// invoices, late credited usage, per-group rounding, and invalid credit amounts.
// Each scenario declares its exact storage facts and literal invoice snapshot.
func TestAssembleInvoice(t *testing.T) {
	cases := []struct {
		name             string
		month            string
		header           Invoice
		usage            []invoiceUsageFixture
		addons           []invoiceAddon
		wantInvoice      Invoice
		wantError        bool
		wantErrorMessage string
	}{
		{
			name:  "empty month still displays the zero credit line",
			month: "2026-11", header: Invoice{CustomerID: "acme", Number: "ACME-0002", Month: "2026-11"},
			wantInvoice: Invoice{
				CustomerID: "acme", Number: "ACME-0002", Month: "2026-11",
				Lines:      []InvoiceLine{{Kind: "credit", Description: "Used credit", AmountCents: 0}},
				TotalCents: 0, GrossUsageTicks: "0", CreditUsedTicks: "0",
			},
		},
		{
			name:  "credited late usage keeps its original month and excludes the add-on from credit",
			month: "2026-11", header: Invoice{CustomerID: "acme", Number: "ACME-0002", Month: "2026-11"},
			usage: []invoiceUsageFixture{
				{
					priceVersionID: "acme-price",
					metric:         "cpu",
					usageMonth:     "2026-10",
					units:          "50000000",
					grossTicks:     "200000000",
					creditTicks:    "200000000",
				},
				{
					priceVersionID: "acme-price",
					metric:         "cpu",
					usageMonth:     "2026-11",
					units:          "100000000",
					grossTicks:     "400000000",
					creditTicks:    "400000000",
				},
			},
			addons: []invoiceAddon{{SubscriptionID: "pack", Name: "concurrency_pack", PriceCents: 2_000}},
			wantInvoice: Invoice{
				CustomerID: "acme", Number: "ACME-0002", Month: "2026-11",
				Lines: []InvoiceLine{
					{
						Kind:           "usage",
						Description:    "Late usage from 2026-10",
						AmountCents:    200,
						PriceVersionID: "acme-price",
						Metric:         "cpu",
						UsageMonth:     "2026-10",
						Units:          "50000000",
						GrossTicks:     "200000000",
						CreditTicks:    "200000000",
					},
					{
						Kind:           "usage",
						Description:    "Usage",
						AmountCents:    400,
						PriceVersionID: "acme-price",
						Metric:         "cpu",
						UsageMonth:     "2026-11",
						Units:          "100000000",
						GrossTicks:     "400000000",
						CreditTicks:    "400000000",
					},
					{Kind: "addon", Description: "concurrency_pack", AmountCents: 2_000, SubscriptionID: "pack"},
					{Kind: "credit", Description: "Used credit", AmountCents: -600},
				},
				TotalCents: 2_000, GrossUsageTicks: "600000000", CreditUsedTicks: "600000000",
			},
		},
		{
			name:  "group rounding derives the credit line from gross minus net cents",
			month: "2026-11", header: Invoice{CustomerID: "cyberdyne", Number: "CYBERDYNE-0002", Month: "2026-11"},
			usage: []invoiceUsageFixture{
				{
					priceVersionID: "first-price",
					metric:         "cpu",
					usageMonth:     "2026-11",
					units:          "100000",
					grossTicks:     "500000",
					creditTicks:    "200000",
				},
				{
					priceVersionID: "second-price",
					metric:         "cpu",
					usageMonth:     "2026-11",
					units:          "100000",
					grossTicks:     "500000",
					creditTicks:    "0",
				},
			},
			wantInvoice: Invoice{
				CustomerID: "cyberdyne", Number: "CYBERDYNE-0002", Month: "2026-11",
				Lines: []InvoiceLine{
					{
						Kind:           "usage",
						Description:    "Usage",
						AmountCents:    1,
						PriceVersionID: "first-price",
						Metric:         "cpu",
						UsageMonth:     "2026-11",
						Units:          "100000",
						GrossTicks:     "500000",
						CreditTicks:    "200000",
					},
					{
						Kind:           "usage",
						Description:    "Usage",
						AmountCents:    1,
						PriceVersionID: "second-price",
						Metric:         "cpu",
						UsageMonth:     "2026-11",
						Units:          "100000",
						GrossTicks:     "500000",
						CreditTicks:    "0",
					},
					{Kind: "credit", Description: "Used credit", AmountCents: -1},
				},
				TotalCents: 1, GrossUsageTicks: "1000000", CreditUsedTicks: "200000",
			},
		},
		{
			name:  "credit greater than the group's gross is rejected",
			month: "2026-11", header: Invoice{CustomerID: "acme", Month: "2026-11"},
			usage: []invoiceUsageFixture{
				{priceVersionID: "price", metric: "cpu", usageMonth: "2026-11", units: "1", grossTicks: "1", creditTicks: "2"},
			},
			wantError: true, wantErrorMessage: "allocated credit exceeds gross charge",
		},
		{
			name:  "aggregate displayed credit cannot overflow signed cents",
			month: "2026-11", header: Invoice{CustomerID: "acme", Month: "2026-11"},
			usage: []invoiceUsageFixture{
				{
					priceVersionID: "large-price",
					metric:         "cpu",
					usageMonth:     "2026-11",
					units:          "9223372036854775807",
					grossTicks:     "9223372036854775807000000",
					creditTicks:    "9223372036854775807000000",
				},
				{
					priceVersionID: "small-price",
					metric:         "cpu",
					usageMonth:     "2026-11",
					units:          "1",
					grossTicks:     "1000000",
					creditTicks:    "1000000",
				},
			},
			wantError: true, wantErrorMessage: "invoice cents exceed the signed 64-bit range",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			charges := invoiceCharges{Addons: tc.addons}
			for _, fixture := range tc.usage {
				charges.Usage = append(charges.Usage, invoiceUsageInput(t, fixture))
			}

			got, err := assembleInvoice(tc.header, invoiceMonthInput(t, tc.month), charges)
			if tc.wantError {
				if err == nil {
					t.Fatalf("assembleInvoice(%s, %+v) error = nil; want %q", tc.month, tc.usage, tc.wantErrorMessage)
				}

				if err.Error() != tc.wantErrorMessage {
					t.Errorf("assembleInvoice(%s, %+v) error = %q; want %q", tc.month, tc.usage, err, tc.wantErrorMessage)
				}

				return
			}

			if err != nil {
				t.Fatalf("assembleInvoice(%s, %+v) error = %v; want nil", tc.month, tc.usage, err)
			}

			if !reflect.DeepEqual(got, tc.wantInvoice) {
				t.Errorf("assembleInvoice(%s, usage=%+v, addons=%+v) = %+v; want %+v", tc.month, tc.usage, tc.addons, got, tc.wantInvoice)
			}
		})
	}
}

// TestInvoiceTotal verifies the final signed line sum, including a gross subtotal
// that exceeds int64 before credit brings it back into range. Literal line inputs
// make overflow, negative totals, and the inclusive upper boundary visible.
func TestInvoiceTotal(t *testing.T) {
	cases := []struct {
		name             string
		lines            []InvoiceLine
		wantTotalCents   int64
		wantError        bool
		wantErrorMessage string
	}{
		{
			name:           "usage and add-on remain after the credit deduction",
			lines:          []InvoiceLine{{Kind: "usage", AmountCents: 600}, {Kind: "addon", AmountCents: 2_000}, {Kind: "credit", AmountCents: -600}},
			wantTotalCents: 2_000,
		},
		{
			name:           "later credit brings the positive subtotal back to the maximum total",
			lines:          []InvoiceLine{{Kind: "usage", AmountCents: math.MaxInt64}, {Kind: "addon", AmountCents: 1}, {Kind: "credit", AmountCents: -1}},
			wantTotalCents: 9_223_372_036_854_775_807,
		},
		{
			name:      "final total above int64 is rejected",
			lines:     []InvoiceLine{{Kind: "addon", AmountCents: math.MaxInt64}, {Kind: "addon", AmountCents: 1}, {Kind: "credit", AmountCents: 0}},
			wantError: true, wantErrorMessage: "invoice total is outside nonnegative signed 64-bit cents",
		},
		{
			name:      "negative final total is rejected",
			lines:     []InvoiceLine{{Kind: "credit", AmountCents: -1}},
			wantError: true, wantErrorMessage: "invoice total is outside nonnegative signed 64-bit cents",
		},
		{
			name:  "no financial lines sum to zero",
			lines: []InvoiceLine{}, wantTotalCents: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := invoiceTotal(tc.lines)
			if tc.wantError {
				if err == nil {
					t.Fatalf("invoiceTotal(%+v) error = nil; want %q", tc.lines, tc.wantErrorMessage)
				}

				if err.Error() != tc.wantErrorMessage {
					t.Errorf("invoiceTotal(%+v) error = %q; want %q", tc.lines, err, tc.wantErrorMessage)
				}

				return
			}

			if err != nil {
				t.Fatalf("invoiceTotal(%+v) error = %v; want nil", tc.lines, err)
			}

			if got != tc.wantTotalCents {
				t.Errorf("invoiceTotal(%+v) = %d cents; want %d cents", tc.lines, got, tc.wantTotalCents)
			}
		})
	}
}

// invoiceUsageInput decodes literal input ticks and months for the pure invoice
// builder. It performs no rating, allocation, rounding, or expected-value calculation.
func invoiceUsageInput(t *testing.T, fixture invoiceUsageFixture) invoiceUsage {
	t.Helper()

	return invoiceUsage{
		PriceVersionID: fixture.priceVersionID, Metric: fixture.metric,
		UsageMonth: invoiceMonthInput(t, fixture.usageMonth), Units: fixture.units,
		Gross: invoiceTicksInput(t, fixture.grossTicks), Credit: invoiceTicksInput(t, fixture.creditTicks),
	}
}

// invoiceMonthInput parses one explicit UTC month without consulting billing
// routing rules, so late-usage inputs stay visible in their scenario declaration.
func invoiceMonthInput(t *testing.T, value string) time.Time {
	t.Helper()
	month, err := time.Parse("2006-01", value)
	if err != nil {
		t.Fatalf("Parse invoice input month %q: %v", value, err)
	}

	return month
}

// invoiceTicksInput converts a literal arbitrary-precision input to an Amount;
// expected invoice ticks and displayed cents are always separate literal values.
func invoiceTicksInput(t *testing.T, value string) accounting.Amount {
	t.Helper()
	ticks, ok := new(big.Int).SetString(value, 10)
	if !ok {
		t.Fatalf("Parse invoice input ticks %q: invalid integer", value)
	}

	amount, err := accounting.FromTicks(ticks)
	if err != nil {
		t.Fatalf("Construct invoice input amount from %q ticks: %v", value, err)
	}

	return amount
}
