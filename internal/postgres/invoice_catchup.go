package postgres

import (
	"context"
	"errors"
	"time"

	"e2b/billing-api/internal/billing"
	"github.com/jackc/pgx/v5"
)

const invoiceCatchupScopePrefix = "invoice-catch-up/"

// One year bounds the transaction and account lock; longer gaps use consecutive commands.
const maxInvoiceCatchupMonths = 12

const monthsPerCalendarYear = 12

// CatchUpInvoices closes each missing completed month in one atomic transaction.
// The command key freezes the returned range, including retries after later closes.
func (s *Store) CatchUpInvoices(ctx context.Context, customer, key, throughValue string) (billing.InvoiceRun, error) {
	if err := billing.ValidateIdentifier("customer_id", customer); err != nil {
		return billing.InvoiceRun{}, err
	}

	if err := billing.ValidateIdempotencyKey(key); err != nil {
		return billing.InvoiceRun{}, err
	}

	through, err := billing.ParseMonth(throughValue)
	if err != nil {
		var validation *billing.ValidationError
		if errors.As(err, &validation) {
			return billing.InvoiceRun{}, &billing.ValidationError{Field: "through_month", Message: validation.Message}
		}

		return billing.InvoiceRun{}, err
	}

	scope := invoiceCatchupScopePrefix + customer
	var result billing.InvoiceRun
	err = s.transact(ctx, func(tx pgx.Tx) error {
		found, err := operationResponse(ctx, tx, scope, key, &result)
		if err != nil {
			return err
		}

		if found {
			if result.ThroughMonth != throughValue {
				return billing.ErrConflict
			}

			return nil
		}

		if _, err := lockAccount(ctx, tx, customer); err != nil {
			return err
		}

		issuedAt := s.now().UTC().Truncate(time.Microsecond)
		result, err = catchUpInTransaction(ctx, tx, customer, through, issuedAt)
		if err != nil {
			return err
		}

		return recordOperation(ctx, tx, scope, key, map[string]string{"through_month": throughValue}, result, issuedAt)
	})

	return result, err
}

// catchUpInTransaction retains existing snapshots and freezes a chronological
// range under the caller's account lock. A failure leaves every month open.
func catchUpInTransaction(ctx context.Context, tx pgx.Tx, customer string, through, issuedAt time.Time) (billing.InvoiceRun, error) {
	result := billing.InvoiceRun{CustomerID: customer, ThroughMonth: through.Format("2006-01"), Invoices: []billing.Invoice{}}
	previous, err := invoiceInTransaction(ctx, tx, customer, through)
	if err == nil {
		result.Invoices = append(result.Invoices, previous)
		return result, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}

	if issuedAt.Before(through.AddDate(0, 1, 0)) {
		return result, &billing.ValidationError{Field: "through_month", Message: "Only completed UTC calendar months can be closed."}
	}

	first, err := firstMissingInvoiceMonth(ctx, tx, customer, through)
	if err != nil {
		return result, err
	}

	months := (through.Year()-first.Year())*monthsPerCalendarYear + int(through.Month()-first.Month()) + 1
	if months > maxInvoiceCatchupMonths {
		return result, &billing.ValidationError{Field: "through_month", Message: "A catch-up command may close at most 12 months. Request an earlier through_month first."}
	}

	for month := first; !month.After(through); month = month.AddDate(0, 1, 0) {
		if err := validateClosing(ctx, tx, customer, month, issuedAt); err != nil {
			return result, err
		}

		invoice, err := buildInvoice(ctx, tx, customer, month, issuedAt)
		if err != nil {
			return result, err
		}

		if err := freezeInvoice(ctx, tx, invoice, month); err != nil {
			return result, err
		}

		result.Invoices = append(result.Invoices, invoice)
	}

	return result, nil
}

// firstMissingInvoiceMonth begins after the latest closure, or at the earliest
// known receipt, rated group or purchase on a customer's first run. UTC receipt
// months include pending and quarantined events so a missing month is visible.
func firstMissingInvoiceMonth(ctx context.Context, tx pgx.Tx, customer string, through time.Time) (time.Time, error) {
	var latest *time.Time
	if err := tx.QueryRow(ctx, "SELECT max(billing_month) FROM closed_billing_months WHERE customer_id=$1", customer).Scan(&latest); err != nil {
		return time.Time{}, err
	}

	if latest != nil {
		if !latest.Before(through) {
			return time.Time{}, billing.ErrConflict
		}

		return latest.AddDate(0, 1, 0), nil
	}

	var first time.Time
	err := tx.QueryRow(ctx, `SELECT least($2::date, min(month)) FROM (
  SELECT date_trunc('month', period_start AT TIME ZONE 'UTC')::date AS month FROM usage_inbox WHERE customer_id=$1
  UNION ALL SELECT billing_month FROM rated_usage_groups WHERE customer_id=$1
  UNION ALL SELECT start_month FROM addon_subscriptions WHERE customer_id=$1
 ) obligations`, customer, through).Scan(&first)

	return first, err
}
