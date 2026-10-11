package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"e2b/billing-api/internal/billing"
	"github.com/jackc/pgx/v5"
)

// CloseMonth invoices only usage already processed when it acquires the account
// lock. It never processes pending usage or waits for the inbox to drain; the
// worker bills it in an eligible open month after closure. Publication is one
// atomic transaction, and a retry reads the original invoice. A new closure
// requires an ended UTC month, cannot skip earlier existing data on the first
// invoice, and requires the preceding month closed on subsequent invoices.
func (s *Store) CloseMonth(ctx context.Context, customer, monthValue string) (billing.Invoice, error) {
	if err := billing.ValidateIdentifier("customer_id", customer); err != nil {
		return billing.Invoice{}, err
	}

	month, err := billing.ParseMonth(monthValue)
	if err != nil {
		return billing.Invoice{}, err
	}

	return s.issueInvoice(ctx, customer, month)
}

// issueInvoice owns the final atomic publication boundary. An identical retry
// reads the frozen snapshot before constructing or numbering another invoice.
func (s *Store) issueInvoice(ctx context.Context, customer string, month time.Time) (billing.Invoice, error) {
	var result billing.Invoice
	err := s.transact(ctx, func(tx pgx.Tx) error {
		if _, err := lockAccount(ctx, tx, customer); err != nil {
			return err
		}

		previous, err := invoiceInTransaction(ctx, tx, customer, month)
		if err == nil {
			result = previous
			return nil
		}

		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		issuedAt := s.now().UTC().Truncate(time.Microsecond)
		if err := validateClosing(ctx, tx, customer, month, issuedAt); err != nil {
			return err
		}

		result, err = buildInvoice(ctx, tx, customer, month, issuedAt)
		if err != nil {
			return err
		}

		return freezeInvoice(ctx, tx, result, month)
	})

	return result, err
}

func (s *Store) Invoice(ctx context.Context, customer, monthValue string) (billing.Invoice, error) {
	if err := billing.ValidateIdentifier("customer_id", customer); err != nil {
		return billing.Invoice{}, err
	}

	month, err := billing.ParseMonth(monthValue)
	if err != nil {
		return billing.Invoice{}, err
	}

	var encoded []byte
	err = s.pool.QueryRow(ctx, "SELECT snapshot FROM invoices WHERE customer_id=$1 AND billing_month=$2", customer, month).Scan(&encoded)
	if errors.Is(err, pgx.ErrNoRows) {
		return billing.Invoice{}, billing.ErrNotFound
	}

	if err != nil {
		return billing.Invoice{}, err
	}

	var result billing.Invoice
	err = json.Unmarshal(encoded, &result)

	return result, err
}

func invoiceInTransaction(ctx context.Context, tx pgx.Tx, customer string, month time.Time) (billing.Invoice, error) {
	var encoded []byte
	err := tx.QueryRow(ctx, "SELECT snapshot FROM invoices WHERE customer_id=$1 AND billing_month=$2", customer, month).Scan(&encoded)
	if err != nil {
		return billing.Invoice{}, err
	}

	var result billing.Invoice
	err = json.Unmarshal(encoded, &result)

	return result, err
}
