package billing

import (
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"e2b/billing-api/internal/accounting"
)

// invoiceHeader freezes buyer details and the reserved number without reading
// storage. The caller holds the account lock until the complete invoice commits.
func invoiceHeader(customer string, month, issuedAt time.Time, buyer invoiceBuyer) (Invoice, error) {
	invoice := Invoice{
		CustomerID: customer, Month: month.Format("2006-01"), Currency: "USD",
		CustomerName: buyer.Name, Country: buyer.Country, BillingAddress: buyer.BillingAddress,
		IssuedAt: issuedAt, Lines: []InvoiceLine{},
	}
	if buyer.NextNumber == math.MaxInt64 {
		return invoice, fmt.Errorf("invoice sequence exhausted")
	}
	invoice.Number = fmt.Sprintf("%s-%04d", strings.ToUpper(customer), buyer.NextNumber)
	return invoice, nil
}

// assembleInvoice applies presentation rules to already loaded financial facts.
// It neither reads storage nor allocates credit; the exact ledger is unchanged.
func assembleInvoice(invoice Invoice, month time.Time, charges invoiceCharges) (Invoice, error) {
	usage, err := invoiceUsageLines(month, charges.Usage)
	if err != nil {
		return invoice, err
	}
	invoice.Lines = append(invoice.Lines, usage.Lines...)
	invoice.GrossUsageTicks = usage.Gross.Ticks().String()
	invoice.CreditUsedTicks = usage.Credit.Ticks().String()
	for _, addon := range charges.Addons {
		invoice.Lines = append(invoice.Lines, InvoiceLine{
			Kind: "addon", SubscriptionID: addon.SubscriptionID,
			Description: addon.Name, AmountCents: addon.PriceCents,
		})
	}
	invoice.Lines = append(invoice.Lines, InvoiceLine{
		Kind: "credit", Description: "Used credit", AmountCents: -usage.DeductionCents,
	})
	invoice.TotalCents, err = invoiceTotal(invoice.Lines)
	return invoice, err
}

type invoiceUsageSummary struct {
	Lines          []InvoiceLine
	Gross          accounting.Amount
	Credit         accounting.Amount
	DeductionCents int64
}

// invoiceUsageLines preserves query order and rounds each price group separately.
// Exact gross and credit totals remain available alongside the displayed cents.
func invoiceUsageLines(month time.Time, groups []invoiceUsage) (invoiceUsageSummary, error) {
	var summary invoiceUsageSummary
	for _, group := range groups {
		line, deduction, err := invoiceUsageLine(month, group)
		if err != nil {
			return summary, err
		}
		summary.DeductionCents, err = addCents(summary.DeductionCents, deduction)
		if err != nil {
			return summary, err
		}
		summary.Lines = append(summary.Lines, line)
		summary.Gross = summary.Gross.Add(group.Gross)
		summary.Credit = summary.Credit.Add(group.Credit)
	}
	return summary, nil
}

func invoiceUsageLine(month time.Time, group invoiceUsage) (InvoiceLine, int64, error) {
	display, err := accounting.InvoiceAmounts(group.Gross, group.Credit)
	if err != nil {
		return InvoiceLine{}, 0, err
	}
	line := InvoiceLine{
		Kind: "usage", Description: "Usage", AmountCents: display.GrossCents,
		PriceVersionID: group.PriceVersionID, Metric: group.Metric,
		UsageMonth: group.UsageMonth.Format("2006-01"), Units: group.Units,
		GrossTicks: group.Gross.Ticks().String(), CreditTicks: group.Credit.Ticks().String(),
	}
	if group.UsageMonth.Before(month) {
		line.Description = "Late usage from " + line.UsageMonth
	}
	return line, display.CreditCents, nil
}

// invoiceTotal checks the final sum rather than intermediate positive subtotals:
// a later credit line can bring a large gross subtotal back inside int64 cents.
func invoiceTotal(lines []InvoiceLine) (int64, error) {
	total := new(big.Int)
	for _, line := range lines {
		total.Add(total, big.NewInt(line.AmountCents))
	}
	if !total.IsInt64() || total.Sign() < 0 {
		return 0, fmt.Errorf("invoice total is outside nonnegative signed 64-bit cents")
	}
	return total.Int64(), nil
}

func addCents(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, fmt.Errorf("invoice cents exceed the signed 64-bit range")
	}
	return a + b, nil
}
