package billing

import (
	"context"
	"time"

	"e2b/billing-api/internal/accounting"
	"github.com/jackc/pgx/v5"
)

// These storage facts deliberately contain no invoice presentation fields.
// Database decoding and financial presentation can change independently.
type invoiceBuyer struct {
	Name           string
	Country        string
	BillingAddress string
	NextNumber     int64
}

type invoiceUsage struct {
	PriceVersionID string
	Metric         string
	UsageMonth     time.Time
	Units          string
	Gross          accounting.Amount
	Credit         accounting.Amount
}

type invoiceAddon struct {
	SubscriptionID string
	Name           string
	PriceCents     int64
}

type invoiceCharges struct {
	Usage  []invoiceUsage
	Addons []invoiceAddon
}

// buildInvoice reads all facts through the caller's locked transaction, then
// delegates financial presentation to pure functions before anything is frozen.
func buildInvoice(ctx context.Context, tx pgx.Tx, customer string, month time.Time) (Invoice, error) {
	issuedAt := time.Now().UTC().Truncate(time.Microsecond)
	buyer, err := loadInvoiceBuyer(ctx, tx, customer)
	if err != nil {
		return Invoice{}, err
	}
	invoice, err := invoiceHeader(customer, month, issuedAt, buyer)
	if err != nil {
		return invoice, err
	}
	charges, err := loadInvoiceCharges(ctx, tx, customer, month)
	if err != nil {
		return invoice, err
	}
	return assembleInvoice(invoice, month, charges)
}

func loadInvoiceBuyer(ctx context.Context, tx pgx.Tx, customer string) (invoiceBuyer, error) {
	var buyer invoiceBuyer
	err := tx.QueryRow(ctx, `SELECT c.name,c.country,c.billing_address,s.next_invoice_number
        FROM customers c JOIN customer_billing_state s USING(customer_id) WHERE customer_id=$1`, customer).
		Scan(&buyer.Name, &buyer.Country, &buyer.BillingAddress, &buyer.NextNumber)
	return buyer, err
}

func loadInvoiceCharges(ctx context.Context, tx pgx.Tx, customer string, month time.Time) (invoiceCharges, error) {
	usage, err := loadInvoiceUsage(ctx, tx, customer, month)
	if err != nil {
		return invoiceCharges{}, err
	}
	addons, err := loadInvoiceAddons(ctx, tx, customer, month)
	if err != nil {
		return invoiceCharges{}, err
	}
	return invoiceCharges{Usage: usage, Addons: addons}, nil
}

func loadInvoiceUsage(ctx context.Context, tx pgx.Tx, customer string, month time.Time) ([]invoiceUsage, error) {
	rows, err := tx.Query(ctx, `SELECT price_version_id,metric,usage_month,total_units::text,
        exact_charge_ticks::text,allocated_credit_ticks::text FROM rated_usage_groups
        WHERE customer_id=$1 AND billing_month=$2 ORDER BY usage_month,metric,price_version_id`, customer, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []invoiceUsage
	for rows.Next() {
		group, err := scanInvoiceUsage(rows)
		if err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

// scanInvoiceUsage rejects corrupt numeric storage before it enters financial
// presentation. The DTO carries exact amounts rather than database text.
func scanInvoiceUsage(row pgx.Row) (invoiceUsage, error) {
	var group invoiceUsage
	var grossTicks, creditTicks string
	if err := row.Scan(&group.PriceVersionID, &group.Metric, &group.UsageMonth, &group.Units, &grossTicks, &creditTicks); err != nil {
		return group, err
	}
	var err error
	group.Gross, err = amount(grossTicks)
	if err != nil {
		return group, err
	}
	group.Credit, err = amount(creditTicks)
	return group, err
}

func loadInvoiceAddons(ctx context.Context, tx pgx.Tx, customer string, month time.Time) ([]invoiceAddon, error) {
	rows, err := tx.Query(ctx, `SELECT subscription_id,addon_name,monthly_price_cents
        FROM addon_subscriptions WHERE customer_id=$1 AND start_month<=$2 ORDER BY addon_name,subscription_id`, customer, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var addons []invoiceAddon
	for rows.Next() {
		var addon invoiceAddon
		if err := rows.Scan(&addon.SubscriptionID, &addon.Name, &addon.PriceCents); err != nil {
			return nil, err
		}
		addons = append(addons, addon)
	}
	return addons, rows.Err()
}
