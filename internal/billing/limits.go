package billing

import (
	"context"
	"errors"
	"time"

	"e2b/billing-api/internal/accounting"
	"github.com/jackc/pgx/v5"
)

type SpendLimitChange struct {
	OperationID string `json:"operation_id"`
	LimitCents  *int64 `json:"limit_cents"`
}

type LimitStatus struct {
	CustomerID       string `json:"customer_id"`
	Month            string `json:"month"`
	LimitCents       *int64 `json:"limit_cents"`
	GrossChargeTicks string `json:"gross_charge_ticks"`
	LimitReached     bool   `json:"limit_reached"`
	StateVersion     int64  `json:"state_version"`
	PendingEvents    int64  `json:"pending_events"`
	ProcessingErrors int64  `json:"processing_errors"`
}

// ParseMonth requires a canonical UTC calendar month, including supported years.
func ParseMonth(value string) (time.Time, error) {
	month, err := time.Parse("2006-01", value)
	if err != nil || month.Format("2006-01") != value {
		return time.Time{}, &ValidationError{Field: "month", Message: "Use a UTC calendar month as YYYY-MM."}
	}

	if err := validateTime("month", month); err != nil {
		return time.Time{}, err
	}

	return month, nil
}

// SetSpendLimit records each operation once. Replaying an old operation returns
// its original result while preserving the limit set by any newer operation.
func (s *Store) SetSpendLimit(ctx context.Context, customer string, change SpendLimitChange) error {
	if err := ValidateIdentifier("customer_id", customer); err != nil {
		return err
	}

	if err := ValidateIdentifier("operation_id", change.OperationID); err != nil {
		return err
	}

	if change.LimitCents != nil {
		if err := validateCents("limit_cents", *change.LimitCents, false); err != nil {
			return err
		}
	}

	return s.transact(ctx, func(tx pgx.Tx) error {
		if _, err := lockAccount(ctx, tx, customer); err != nil {
			return err
		}

		var previous *int64
		err := tx.QueryRow(ctx, "SELECT limit_cents FROM spend_limit_operations WHERE customer_id=$1 AND operation_id=$2", customer, change.OperationID).Scan(&previous)
		if err == nil {
			if equalLimit(previous, change.LimitCents) {
				return nil
			}

			return ErrConflict
		}

		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		if _, err := tx.Exec(ctx, "INSERT INTO spend_limit_operations VALUES ($1,$2,$3,$4)", customer, change.OperationID, change.LimitCents, time.Now().UTC().Truncate(time.Microsecond)); err != nil {
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
func (s *Store) MonthlyLimitStatus(ctx context.Context, customer, monthValue string) (LimitStatus, error) {
	if err := ValidateIdentifier("customer_id", customer); err != nil {
		return LimitStatus{}, err
	}

	month, err := ParseMonth(monthValue)
	if err != nil {
		return LimitStatus{}, err
	}

	result := LimitStatus{CustomerID: customer, Month: monthValue}
	err = s.pool.QueryRow(ctx, `SELECT spend_limit_cents,state_version,
        COALESCE((SELECT gross_charge_ticks::text FROM monthly_usage WHERE customer_id=$1 AND usage_month=$2),'0'),
        (SELECT count(*) FROM usage_inbox WHERE customer_id=$1 AND processed_at IS NULL AND processing_error IS NULL),
        (SELECT count(*) FROM usage_inbox WHERE customer_id=$1 AND processing_error IS NOT NULL)
        FROM customer_billing_state WHERE customer_id=$1`, customer, month).
		Scan(&result.LimitCents, &result.StateVersion, &result.GrossChargeTicks, &result.PendingEvents, &result.ProcessingErrors)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, ErrNotFound
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
