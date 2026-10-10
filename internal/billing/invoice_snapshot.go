package billing

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// freezeInvoice publishes the snapshot, group freeze, closure, sequence, and
// version together. Issuance never consumes credit a second time.
func freezeInvoice(ctx context.Context, tx pgx.Tx, invoice Invoice, month time.Time) error {
	encoded, err := json.Marshal(invoice)
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, "INSERT INTO closed_billing_months VALUES ($1,$2,$3)", invoice.CustomerID, month, invoice.IssuedAt); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, "INSERT INTO invoices VALUES ($1,$2,$3,$4,$5)", invoice.CustomerID, month, invoice.Number, invoice.TotalCents, encoded); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `INSERT INTO invoiced_usage_groups
        SELECT group_id,customer_id,billing_month FROM rated_usage_groups
        WHERE customer_id=$1 AND billing_month=$2`, invoice.CustomerID, month); err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `UPDATE customer_billing_state SET next_invoice_number=next_invoice_number+1,
        state_version=state_version+1 WHERE customer_id=$1`, invoice.CustomerID)

	return err
}
