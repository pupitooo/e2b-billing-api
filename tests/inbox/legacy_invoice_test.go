//go:build integration

package inbox_test

import (
	"context"
	"e2b/billing-api/internal/billing"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
)

// publishLegacyInvoice installs a caller-declared historical snapshot using the
// pre-cancellation schema. Migration tests supply every financial field literally;
// current invoice queries require the complete migrated schema.
func publishLegacyInvoice(t *testing.T, pool *pgxpool.Pool, invoice billing.Invoice) {
	t.Helper()
	data, err := json.Marshal(invoice)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	month := invoice.Month + "-01"
	if _, err := tx.Exec(ctx, "INSERT INTO closed_billing_months VALUES ($1,$2,$3)", invoice.CustomerID, month, invoice.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO invoices VALUES ($1,$2,$3,$4,$5)", invoice.CustomerID, month, invoice.Number, invoice.TotalCents, data); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO invoiced_usage_groups SELECT group_id,customer_id,billing_month
  FROM rated_usage_groups WHERE customer_id=$1 AND billing_month=$2`, invoice.CustomerID, month); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "UPDATE customer_billing_state SET next_invoice_number=next_invoice_number+1,state_version=state_version+1 WHERE customer_id=$1", invoice.CustomerID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
