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
	priceVersionID   string
	metric           string
	usageMonth       string
	units            string
	grossMicrocents  string
	creditMicrocents string
}

// TestAssembleInvoice verifies financial presentation without PostgreSQL: empty
// invoices, retained zero-cent usage, late credited usage, per-group rounding,
// and invalid credit amounts.
// Each scenario declares its exact storage facts and literal invoice snapshot.
func TestAssembleInvoice(t *testing.T) {
	cases := []struct {
		name             string
		month            string
		header           Invoice
		usage            []invoiceUsageFixture
		addons           []InvoiceAddon
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
				TotalCents: 0, GrossUsageMicrocents: "0", CreditUsedMicrocents: "0",
			},
		},
		{
			name:  "zero-cent usage lines retain units and exact charges for each price group",
			month: "2026-10", header: Invoice{CustomerID: "acme", Number: "ACME-0001", Month: "2026-10"},
			usage: []invoiceUsageFixture{
				{priceVersionID: "first-price", metric: "cpu_seconds", usageMonth: "2026-10", units: "75000", grossMicrocents: "300000", creditMicrocents: "0"},
				{priceVersionID: "second-price", metric: "cpu_seconds", usageMonth: "2026-10", units: "60000", grossMicrocents: "300000", creditMicrocents: "0"},
			},
			wantInvoice: Invoice{
				CustomerID: "acme", Number: "ACME-0001", Month: "2026-10",
				Lines: []InvoiceLine{
					{Kind: "usage", Description: "Usage", AmountCents: 0, PriceVersionID: "first-price", Metric: "cpu_seconds", UsageMonth: "2026-10", Units: "75000", GrossMicrocents: "300000", CreditMicrocents: "0"},
					{Kind: "usage", Description: "Usage", AmountCents: 0, PriceVersionID: "second-price", Metric: "cpu_seconds", UsageMonth: "2026-10", Units: "60000", GrossMicrocents: "300000", CreditMicrocents: "0"},
					{Kind: "credit", Description: "Used credit", AmountCents: 0},
				},
				TotalCents: 0, GrossUsageMicrocents: "600000", CreditUsedMicrocents: "0",
			},
		},
		{
			name:  "credited late usage keeps its original month and excludes the add-on from credit",
			month: "2026-11", header: Invoice{CustomerID: "acme", Number: "ACME-0002", Month: "2026-11"},
			usage: []invoiceUsageFixture{
				{
					priceVersionID:   "acme-price",
					metric:           "cpu",
					usageMonth:       "2026-10",
					units:            "50000000",
					grossMicrocents:  "200000000",
					creditMicrocents: "200000000",
				},
				{
					priceVersionID:   "acme-price",
					metric:           "cpu",
					usageMonth:       "2026-11",
					units:            "100000000",
					grossMicrocents:  "400000000",
					creditMicrocents: "400000000",
				},
			},
			addons: []InvoiceAddon{{SubscriptionID: "pack", Name: "concurrency_pack", PriceCents: 2_000}},
			wantInvoice: Invoice{
				CustomerID: "acme", Number: "ACME-0002", Month: "2026-11",
				Lines: []InvoiceLine{
					{
						Kind:             "usage",
						Description:      "Late usage from 2026-10",
						AmountCents:      200,
						PriceVersionID:   "acme-price",
						Metric:           "cpu",
						UsageMonth:       "2026-10",
						Units:            "50000000",
						GrossMicrocents:  "200000000",
						CreditMicrocents: "200000000",
					},
					{
						Kind:             "usage",
						Description:      "Usage",
						AmountCents:      400,
						PriceVersionID:   "acme-price",
						Metric:           "cpu",
						UsageMonth:       "2026-11",
						Units:            "100000000",
						GrossMicrocents:  "400000000",
						CreditMicrocents: "400000000",
					},
					{Kind: "addon", Description: "concurrency_pack", AmountCents: 2_000, SubscriptionID: "pack"},
					{Kind: "credit", Description: "Used credit", AmountCents: -600},
				},
				TotalCents: 2_000, GrossUsageMicrocents: "600000000", CreditUsedMicrocents: "600000000",
			},
		},
		{
			name:  "group rounding derives the credit line from gross minus net cents",
			month: "2026-11", header: Invoice{CustomerID: "cyberdyne", Number: "CYBERDYNE-0002", Month: "2026-11"},
			usage: []invoiceUsageFixture{
				{
					priceVersionID:   "first-price",
					metric:           "cpu",
					usageMonth:       "2026-11",
					units:            "100000",
					grossMicrocents:  "500000",
					creditMicrocents: "200000",
				},
				{
					priceVersionID:   "second-price",
					metric:           "cpu",
					usageMonth:       "2026-11",
					units:            "100000",
					grossMicrocents:  "500000",
					creditMicrocents: "0",
				},
			},
			wantInvoice: Invoice{
				CustomerID: "cyberdyne", Number: "CYBERDYNE-0002", Month: "2026-11",
				Lines: []InvoiceLine{
					{
						Kind:             "usage",
						Description:      "Usage",
						AmountCents:      1,
						PriceVersionID:   "first-price",
						Metric:           "cpu",
						UsageMonth:       "2026-11",
						Units:            "100000",
						GrossMicrocents:  "500000",
						CreditMicrocents: "200000",
					},
					{
						Kind:             "usage",
						Description:      "Usage",
						AmountCents:      1,
						PriceVersionID:   "second-price",
						Metric:           "cpu",
						UsageMonth:       "2026-11",
						Units:            "100000",
						GrossMicrocents:  "500000",
						CreditMicrocents: "0",
					},
					{Kind: "credit", Description: "Used credit", AmountCents: -1},
				},
				TotalCents: 1, GrossUsageMicrocents: "1000000", CreditUsedMicrocents: "200000",
			},
		},
		{
			name:  "credit greater than the group's gross is rejected",
			month: "2026-11", header: Invoice{CustomerID: "acme", Month: "2026-11"},
			usage: []invoiceUsageFixture{
				{priceVersionID: "price", metric: "cpu", usageMonth: "2026-11", units: "1", grossMicrocents: "1", creditMicrocents: "2"},
			},
			wantError: true, wantErrorMessage: "allocated credit exceeds gross charge",
		},
		{
			name:  "aggregate displayed credit cannot overflow signed cents",
			month: "2026-11", header: Invoice{CustomerID: "acme", Month: "2026-11"},
			usage: []invoiceUsageFixture{
				{
					priceVersionID:   "large-price",
					metric:           "cpu",
					usageMonth:       "2026-11",
					units:            "9223372036854775807",
					grossMicrocents:  "9223372036854775807000000",
					creditMicrocents: "9223372036854775807000000",
				},
				{
					priceVersionID:   "small-price",
					metric:           "cpu",
					usageMonth:       "2026-11",
					units:            "1",
					grossMicrocents:  "1000000",
					creditMicrocents: "1000000",
				},
			},
			wantError: true, wantErrorMessage: "invoice cents exceed the signed 64-bit range",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			charges := InvoiceCharges{Addons: tc.addons}
			for _, fixture := range tc.usage {
				charges.Usage = append(charges.Usage, invoiceUsageInput(t, fixture))
			}

			got, err := AssembleInvoice(tc.header, invoiceMonthInput(t, tc.month), charges)
			if tc.wantError {
				if err == nil {
					t.Fatalf("AssembleInvoice(%s, %+v) error = nil; want %q", tc.month, tc.usage, tc.wantErrorMessage)
				}

				if err.Error() != tc.wantErrorMessage {
					t.Errorf("AssembleInvoice(%s, %+v) error = %q; want %q", tc.month, tc.usage, err, tc.wantErrorMessage)
				}

				return
			}

			if err != nil {
				t.Fatalf("AssembleInvoice(%s, %+v) error = %v; want nil", tc.month, tc.usage, err)
			}

			if !reflect.DeepEqual(got, tc.wantInvoice) {
				t.Errorf("AssembleInvoice(%s, usage=%+v, addons=%+v) = %+v; want %+v", tc.month, tc.usage, tc.addons, got, tc.wantInvoice)
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

// invoiceUsageInput decodes literal input microcents and months for the pure invoice
// builder. It performs no rating, allocation, rounding, or expected-value calculation.
func invoiceUsageInput(t *testing.T, fixture invoiceUsageFixture) InvoiceUsage {
	t.Helper()

	return InvoiceUsage{
		PriceVersionID: fixture.priceVersionID, Metric: fixture.metric,
		UsageMonth: invoiceMonthInput(t, fixture.usageMonth), Units: fixture.units,
		Gross: invoiceMicrocentsInput(t, fixture.grossMicrocents), Credit: invoiceMicrocentsInput(t, fixture.creditMicrocents),
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

// invoiceMicrocentsInput converts a literal arbitrary-precision input to an Amount;
// expected invoice microcents and displayed cents are always separate literal values.
func invoiceMicrocentsInput(t *testing.T, value string) accounting.Amount {
	t.Helper()
	microcents, ok := new(big.Int).SetString(value, 10)
	if !ok {
		t.Fatalf("Parse invoice input microcents %q: invalid integer", value)
	}

	amount, err := accounting.FromMicrocents(microcents)
	if err != nil {
		t.Fatalf("Construct invoice input amount from %q microcents: %v", value, err)
	}

	return amount
}

// TestAddCents verifies exact signed addition at each representable boundary;
// invoice deductions may reach the maximum and must fail only beyond it.
func TestAddCents(t *testing.T) {
	cases := []struct {
		name        string
		left, right int64
		wantCents   int64
		wantError   bool
		wantMessage string
	}{
		{name: "positive maximum is inclusive", left: 9_223_372_036_854_775_806, right: 1, wantCents: 9_223_372_036_854_775_807},
		{name: "positive overflow", left: 9_223_372_036_854_775_807, right: 1, wantError: true, wantMessage: "invoice cents exceed the signed 64-bit range"},
		{name: "negative minimum is inclusive", left: -9_223_372_036_854_775_807, right: -1, wantCents: -9_223_372_036_854_775_808},
		{name: "negative overflow", left: -9_223_372_036_854_775_808, right: -1, wantError: true, wantMessage: "invoice cents exceed the signed 64-bit range"},
		{name: "adding zero to maximum", left: 9_223_372_036_854_775_807, right: 0, wantCents: 9_223_372_036_854_775_807},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := addCents(tc.left, tc.right)
			if (err != nil) != tc.wantError {
				t.Fatalf("addCents(%d, %d): error=%v; wantError=%t", tc.left, tc.right, err, tc.wantError)
			}

			if tc.wantError {
				if err.Error() != tc.wantMessage {
					t.Errorf("addCents(%d, %d): error=%q; want %q", tc.left, tc.right, err, tc.wantMessage)
				}

				return
			}

			if got != tc.wantCents {
				t.Errorf("addCents(%d, %d)=%d; want %d", tc.left, tc.right, got, tc.wantCents)
			}
		})
	}
}
