package billing

import (
	"context"
	"errors"
	"time"

	"e2b/billing-api/internal/usage"
	"github.com/jackc/pgx/v5"
)

func (s *Store) beginClosing(ctx context.Context, customer string, month time.Time) error {
	return s.transact(ctx, func(tx pgx.Tx) error {
		if _, err := lockAccount(ctx, tx, customer); err != nil {
			return err
		}

		if _, err := invoiceInTransaction(ctx, tx, customer, month); err == nil {
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		// Reject unfinished periods before capturing or processing their receipts.
		startedAt := s.now().UTC().Truncate(time.Microsecond)
		if startedAt.Before(month.AddDate(0, 1, 0)) {
			return &ValidationError{Field: "month", Message: "Only completed UTC calendar months can be closed."}
		}

		var later bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM closed_billing_months WHERE customer_id=$1 AND billing_month>=$2)", customer, month).Scan(&later); err != nil {
			return err
		}

		if later {
			return ErrConflict
		}

		tag, err := tx.Exec(ctx, "INSERT INTO invoice_closings VALUES ($1,$2,$3) ON CONFLICT DO NOTHING", customer, month, startedAt)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}

		// This statement's snapshot is the cohort boundary. An earlier receipt
		// timestamp on a still-uncommitted request cannot enter it retroactively.
		_, err = tx.Exec(ctx, `INSERT INTO invoice_closing_receipts
            SELECT $1,$2,source,event_id FROM usage_inbox
            WHERE customer_id=$1 AND period_start<$3 AND processed_at IS NULL`, customer, month, month.AddDate(0, 1, 0))

		return err
	})
}

func (s *Store) drainClosing(ctx context.Context, customer string, month time.Time) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		var candidate receipt
		var processingError *string
		err := s.pool.QueryRow(ctx, `SELECT i.source,i.event_id,i.customer_id,i.processing_error
            FROM invoice_closing_receipts c JOIN usage_inbox i USING(source,event_id)
            WHERE c.customer_id=$1 AND c.billing_month=$2 AND i.processed_at IS NULL
            ORDER BY i.received_at,i.source,i.event_id LIMIT 1`, customer, month).
			Scan(&candidate.event.Source, &candidate.event.EventID, &candidate.event.CustomerID, &processingError)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}

		if err != nil {
			return err
		}

		if processingError != nil {
			return ErrConflict
		}

		if err := s.transact(ctx, func(tx pgx.Tx) error { return processReceipt(ctx, tx, candidate) }); err != nil {
			return err
		}
	}
}

func closingReady(ctx context.Context, tx pgx.Tx, customer string, month time.Time) error {
	var blocked bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM invoice_closing_receipts c
        JOIN usage_inbox i USING(source,event_id)
        WHERE c.customer_id=$1 AND c.billing_month=$2 AND i.processed_at IS NULL)
        OR EXISTS (SELECT 1 FROM rated_usage_groups g WHERE g.customer_id=$1
        AND g.billing_month<$2 AND NOT EXISTS (SELECT 1 FROM closed_billing_months m
        WHERE m.customer_id=$1 AND m.billing_month=g.billing_month))`, customer, month).Scan(&blocked)
	if err != nil {
		return err
	}

	if blocked {
		return ErrConflict
	}

	return nil
}

// routingMonths treats a closing month as closed for receipts outside its fixed
// cohort. Cohort members can still be rated into that month before freezing.
func routingMonths(ctx context.Context, tx pgx.Tx, event usage.Event) ([]time.Time, error) {
	months, err := loadClosedMonths(ctx, tx, event.CustomerID)
	if err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, `SELECT c.billing_month FROM invoice_closings c
        WHERE c.customer_id=$1 AND NOT EXISTS (SELECT 1 FROM invoice_closing_receipts r
        WHERE r.customer_id=c.customer_id AND r.billing_month=c.billing_month AND r.source=$2 AND r.event_id=$3)`, event.CustomerID, event.Source, event.EventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var month time.Time
		if err := rows.Scan(&month); err != nil {
			return nil, err
		}

		months = append(months, month)
	}

	return months, rows.Err()
}
