package billing

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"e2b/billing-api/internal/accounting"
	"github.com/jackc/pgx/v5"
)

func buildInvoice(ctx context.Context, tx pgx.Tx, customer string, month time.Time) (Invoice, error) {
	result := Invoice{CustomerID: customer, Month: month.Format("2006-01"), Currency: "USD", IssuedAt: time.Now().UTC().Truncate(time.Microsecond), Lines: []InvoiceLine{}}
	var number int64
	err := tx.QueryRow(ctx, `SELECT c.name,c.country,c.billing_address,s.next_invoice_number
        FROM customers c JOIN customer_billing_state s USING(customer_id) WHERE customer_id=$1`, customer).
		Scan(&result.CustomerName, &result.Country, &result.BillingAddress, &number)
	if err != nil {
		return result, err
	}
	if number == math.MaxInt64 {
		return result, fmt.Errorf("invoice sequence exhausted")
	}
	result.Number = fmt.Sprintf("%s-%04d", strings.ToUpper(customer), number)
	gross, used, deduction, err := appendUsageLines(ctx, tx, &result, month)
	if err != nil {
		return result, err
	}
	result.GrossUsageTicks = gross.Ticks().String()
	result.CreditUsedTicks = used.Ticks().String()
	if err := appendAddonLines(ctx, tx, &result, month); err != nil {
		return result, err
	}
	result.Lines = append(result.Lines, InvoiceLine{Kind: "credit", Description: "Used credit", AmountCents: -deduction})
	total := new(big.Int)
	for _, line := range result.Lines {
		total.Add(total, big.NewInt(line.AmountCents))
	}
	if !total.IsInt64() || total.Sign() < 0 {
		return result, fmt.Errorf("invoice total is outside nonnegative signed 64-bit cents")
	}
	result.TotalCents = total.Int64()
	return result, nil
}

func appendUsageLines(ctx context.Context, tx pgx.Tx, invoice *Invoice, month time.Time) (accounting.Amount, accounting.Amount, int64, error) {
	rows, err := tx.Query(ctx, `SELECT price_version_id,metric,usage_month,total_units::text,
        exact_charge_ticks::text,allocated_credit_ticks::text FROM rated_usage_groups
        WHERE customer_id=$1 AND billing_month=$2 ORDER BY usage_month,metric,price_version_id`, invoice.CustomerID, month)
	if err != nil {
		return accounting.Amount{}, accounting.Amount{}, 0, err
	}
	defer rows.Close()
	var grossTotal, usedTotal accounting.Amount
	var deduction int64
	for rows.Next() {
		line := InvoiceLine{Kind: "usage", Description: "Usage"}
		var original time.Time
		if err := rows.Scan(&line.PriceVersionID, &line.Metric, &original, &line.Units, &line.GrossTicks, &line.CreditTicks); err != nil {
			return grossTotal, usedTotal, 0, err
		}
		line.UsageMonth = original.Format("2006-01")
		if original.Before(month) {
			line.Description = "Late usage from " + line.UsageMonth
		}
		gross, err := amount(line.GrossTicks)
		if err != nil {
			return grossTotal, usedTotal, 0, err
		}
		used, err := amount(line.CreditTicks)
		if err != nil {
			return grossTotal, usedTotal, 0, err
		}
		display, err := accounting.InvoiceAmounts(gross, used)
		if err != nil {
			return grossTotal, usedTotal, 0, err
		}
		deduction, err = addCents(deduction, display.CreditCents)
		if err != nil {
			return grossTotal, usedTotal, 0, err
		}
		line.AmountCents = display.GrossCents
		invoice.Lines = append(invoice.Lines, line)
		grossTotal = grossTotal.Add(gross)
		usedTotal = usedTotal.Add(used)
	}
	return grossTotal, usedTotal, deduction, rows.Err()
}

func appendAddonLines(ctx context.Context, tx pgx.Tx, invoice *Invoice, month time.Time) error {
	rows, err := tx.Query(ctx, `SELECT subscription_id,addon_name,monthly_price_cents
        FROM addon_subscriptions WHERE customer_id=$1 AND start_month<=$2 ORDER BY addon_name,subscription_id`, invoice.CustomerID, month)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		line := InvoiceLine{Kind: "addon"}
		if err := rows.Scan(&line.SubscriptionID, &line.Description, &line.AmountCents); err != nil {
			return err
		}
		invoice.Lines = append(invoice.Lines, line)
	}
	return rows.Err()
}

func addCents(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, fmt.Errorf("invoice cents exceed the signed 64-bit range")
	}
	return a + b, nil
}
