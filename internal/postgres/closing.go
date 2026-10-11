package postgres

import (
	"context"
	"time"

	"e2b/billing-api/internal/billing"
	"github.com/jackc/pgx/v5"
)

// validateClosing checks eligibility under the same account lock that freezes
// the already processed groups. The first invoice cannot skip known earlier
// usage or active add-on months; subsequent invoices require a closed predecessor.
func validateClosing(ctx context.Context, tx pgx.Tx, customer string, month, issuedAt time.Time) error {
	if issuedAt.Before(month.AddDate(0, 1, 0)) {
		return &billing.ValidationError{Field: "month", Message: "Only completed UTC calendar months can be closed."}
	}

	var conflict bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
        SELECT 1 FROM closed_billing_months WHERE customer_id=$1 AND billing_month>=$2)
        OR (EXISTS (SELECT 1 FROM closed_billing_months WHERE customer_id=$1)
            AND NOT EXISTS (SELECT 1 FROM closed_billing_months
                WHERE customer_id=$1 AND billing_month=$3))
        OR (NOT EXISTS (SELECT 1 FROM closed_billing_months WHERE customer_id=$1)
            AND (EXISTS (SELECT 1 FROM usage_inbox WHERE customer_id=$1
                AND period_start<($2::date::timestamp AT TIME ZONE 'UTC'))
                OR EXISTS (SELECT 1 FROM addon_subscriptions WHERE customer_id=$1
                    AND start_month<$2)))
        OR EXISTS (
            SELECT 1 FROM rated_usage_groups g WHERE g.customer_id=$1 AND g.billing_month<$2
            AND NOT EXISTS (SELECT 1 FROM closed_billing_months m
                WHERE m.customer_id=g.customer_id AND m.billing_month=g.billing_month))`, customer, month, month.AddDate(0, -1, 0)).Scan(&conflict)
	if err != nil {
		return err
	}

	if conflict {
		return billing.ErrConflict
	}

	return nil
}
