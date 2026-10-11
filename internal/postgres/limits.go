package postgres

import (
	"context"
	"errors"
	"time"

	"e2b/billing-api/internal/accounting"
	"e2b/billing-api/internal/billing"
	"github.com/jackc/pgx/v5"
)

// SetSpendLimit records each operation once. Replaying an old operation returns
// its original result while preserving the limit set by any newer operation.
func (s *Store) SetSpendLimit(ctx context.Context, customer string, change billing.SpendLimitChange) error {
	if err := billing.ValidateIdentifier("customer_id", customer); err != nil {
		return err
	}

	if err := billing.ValidateIdempotencyKey(change.IdempotencyKey); err != nil {
		return err
	}

	if change.LimitCents != nil {
		if err := billing.ValidateCents("limit_cents", *change.LimitCents, false); err != nil {
			return err
		}
	}

	return s.transact(ctx, func(tx pgx.Tx) error {
		if _, err := lockAccount(ctx, tx, customer); err != nil {
			return err
		}

		var previous *int64
		err := tx.QueryRow(ctx, "SELECT limit_cents FROM spend_limit_operations WHERE customer_id=$1 AND idempotency_key=$2", customer, change.IdempotencyKey).Scan(&previous)
		if err == nil {
			if equalLimit(previous, change.LimitCents) {
				return nil
			}

			return billing.ErrConflict
		}

		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		if _, err := tx.Exec(ctx, "INSERT INTO spend_limit_operations VALUES ($1,$2,$3,$4)", customer, change.IdempotencyKey, change.LimitCents, s.now().UTC().Truncate(time.Microsecond)); err != nil {
			return err
		}

		_, err = tx.Exec(ctx, "UPDATE customer_billing_state SET spend_limit_cents=$2,state_version=state_version+1 WHERE customer_id=$1", customer, change.LimitCents)

		return err
	})
}

func equalLimit(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}

	return *a == *b
}

// MonthlyLimitStatus uses original-month gross ticks. Credit, add-ons, and the
// month on which late usage is invoiced cannot reduce this month's gross spend.
func (s *Store) MonthlyLimitStatus(ctx context.Context, customer, monthValue string) (billing.LimitStatus, error) {
	if err := billing.ValidateIdentifier("customer_id", customer); err != nil {
		return billing.LimitStatus{}, err
	}

	month, err := billing.ParseMonth(monthValue)
	if err != nil {
		return billing.LimitStatus{}, err
	}

	result := billing.LimitStatus{CustomerID: customer, Month: monthValue}
	err = s.pool.QueryRow(ctx, `SELECT spend_limit_cents,state_version,
        COALESCE((SELECT gross_charge_ticks::text FROM monthly_usage WHERE customer_id=$1 AND usage_month=$2),'0'),
        (SELECT count(*) FROM usage_inbox WHERE customer_id=$1 AND processed_at IS NULL AND processing_error IS NULL),
        (SELECT count(*) FROM usage_inbox WHERE customer_id=$1 AND processing_error IS NOT NULL)
        FROM customer_billing_state WHERE customer_id=$1`, customer, month).
		Scan(&result.LimitCents, &result.StateVersion, &result.GrossChargeTicks, &result.PendingEvents, &result.ProcessingErrors)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, billing.ErrNotFound
	}

	if err != nil {
		return result, err
	}

	gross, err := amount(result.GrossChargeTicks)
	if err != nil {
		return result, err
	}

	result.LimitReached, err = accounting.LimitReached(gross, result.LimitCents)

	return result, err
}
