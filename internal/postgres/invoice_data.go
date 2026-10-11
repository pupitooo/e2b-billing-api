package postgres

import (
	"context"
	"time"

	"e2b/billing-api/internal/billing"
	"github.com/jackc/pgx/v5"
)

// buildInvoice reads all facts through the caller's locked transaction, then
// delegates financial presentation to pure functions before anything is frozen.
func buildInvoice(ctx context.Context, tx pgx.Tx, customer string, month, issuedAt time.Time) (billing.Invoice, error) {
	buyer, err := loadInvoiceBuyer(ctx, tx, customer)
	if err != nil {
		return billing.Invoice{}, err
	}

	invoice, err := billing.InvoiceHeader(customer, month, issuedAt, buyer)
	if err != nil {
		return invoice, err
	}

	charges, err := loadInvoiceCharges(ctx, tx, customer, month)
	if err != nil {
		return invoice, err
	}

	return billing.AssembleInvoice(invoice, month, charges)
}

func loadInvoiceBuyer(ctx context.Context, tx pgx.Tx, customer string) (billing.InvoiceBuyer, error) {
	var buyer billing.InvoiceBuyer
	err := tx.QueryRow(ctx, `SELECT c.name,c.country,c.billing_address,s.next_invoice_number
        FROM customers c JOIN customer_billing_state s USING(customer_id) WHERE customer_id=$1`, customer).
		Scan(&buyer.Name, &buyer.Country, &buyer.BillingAddress, &buyer.NextNumber)

	return buyer, err
}

func loadInvoiceCharges(ctx context.Context, tx pgx.Tx, customer string, month time.Time) (billing.InvoiceCharges, error) {
	usage, err := loadInvoiceUsage(ctx, tx, customer, month)
	if err != nil {
		return billing.InvoiceCharges{}, err
	}

	addons, err := loadInvoiceAddons(ctx, tx, customer, month)
	if err != nil {
		return billing.InvoiceCharges{}, err
	}

	return billing.InvoiceCharges{Usage: usage, Addons: addons}, nil
}

func loadInvoiceUsage(ctx context.Context, tx pgx.Tx, customer string, month time.Time) ([]billing.InvoiceUsage, error) {
	rows, err := tx.Query(ctx, `SELECT price_version_id,metric,usage_month,total_units::text,
        exact_charge_ticks::text,allocated_credit_ticks::text FROM rated_usage_groups
        WHERE customer_id=$1 AND billing_month=$2 ORDER BY usage_month,metric,price_version_id`, customer, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []billing.InvoiceUsage
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
func scanInvoiceUsage(row pgx.Row) (billing.InvoiceUsage, error) {
	var group billing.InvoiceUsage
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

func loadInvoiceAddons(ctx context.Context, tx pgx.Tx, customer string, month time.Time) ([]billing.InvoiceAddon, error) {
	rows, err := tx.Query(ctx, `SELECT subscription_id,addon_name,monthly_price_cents
        FROM addon_subscriptions WHERE customer_id=$1 AND start_month<=$2 ORDER BY addon_name,subscription_id`, customer, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var addons []billing.InvoiceAddon
	for rows.Next() {
		var addon billing.InvoiceAddon
		if err := rows.Scan(&addon.SubscriptionID, &addon.Name, &addon.PriceCents); err != nil {
			return nil, err
		}

		addons = append(addons, addon)
	}

	return addons, rows.Err()
}
