package billing

import (
	"context"
	"errors"
	"time"

	"e2b/billing-api/internal/accounting"
	"github.com/jackc/pgx/v5"
)

type CreditGrant struct {
	OperationID string    `json:"operation_id"`
	AmountCents int64     `json:"amount_cents"`
	RecordedAt  time.Time `json:"recorded_at"`
}

// CreditSnapshot reports exact balance and asynchronous accounting visibility.
// Counts describe committed receipts in the same database statement snapshot.
type CreditSnapshot struct {
	CustomerID       string `json:"customer_id"`
	BalanceTicks     string `json:"credit_balance_ticks"`
	StateVersion     int64  `json:"state_version"`
	PendingEvents    int64  `json:"pending_events"`
	ProcessingErrors int64  `json:"processing_errors"`
}

// GrantCredit uses a stable caller operation identity. Replay returns the grant
// result without increasing the balance or changing earlier usage allocations.
func (s *Store) GrantCredit(ctx context.Context, customer string, grant CreditGrant) error {
	if err := ValidateIdentifier("customer_id", customer); err != nil {
		return err
	}
	if err := ValidateIdentifier("operation_id", grant.OperationID); err != nil {
		return err
	}
	if err := validateCents("amount_cents", grant.AmountCents, true); err != nil {
		return err
	}
	if err := validateTime("recorded_at", grant.RecordedAt); err != nil {
		return err
	}
	ticks, _ := accounting.FromCents(grant.AmountCents)
	return s.transact(ctx, func(tx pgx.Tx) error {
		if _, err := lockAccount(ctx, tx, customer); err != nil {
			return err
		}
		var previous string
		var recorded time.Time
		operation := "grant/" + grant.OperationID
		err := tx.QueryRow(ctx, `SELECT amount_ticks::text,recorded_at FROM credit_entries
            WHERE customer_id=$1 AND operation_id=$2`, customer, operation).Scan(&previous, &recorded)
		if err == nil {
			if previous == ticks.Ticks().String() && recorded.Equal(grant.RecordedAt) {
				return nil
			}
			return ErrConflict
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO credit_entries
            (credit_entry_id,customer_id,operation_id,group_id,amount_ticks,recorded_at)
            VALUES ($1,$2,$3,NULL,$4::numeric,$5)`, identity("grant/", customer, grant.OperationID),
			customer, operation, ticks.Ticks().String(), grant.RecordedAt.UTC())
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE customer_billing_state SET credit_balance_ticks=credit_balance_ticks+$2::numeric,
            state_version=state_version+1 WHERE customer_id=$1`, customer, ticks.Ticks().String())
		return err
	})
}

func (s *Store) Credit(ctx context.Context, customer string) (CreditSnapshot, error) {
	if err := ValidateIdentifier("customer_id", customer); err != nil {
		return CreditSnapshot{}, err
	}
	var snapshot CreditSnapshot
	err := s.pool.QueryRow(ctx, `SELECT customer_id,credit_balance_ticks::text,state_version,
        (SELECT count(*) FROM usage_inbox WHERE customer_id=$1 AND processed_at IS NULL AND processing_error IS NULL),
        (SELECT count(*) FROM usage_inbox WHERE customer_id=$1 AND processing_error IS NOT NULL)
        FROM customer_billing_state WHERE customer_id=$1`, customer).
		Scan(&snapshot.CustomerID, &snapshot.BalanceTicks, &snapshot.StateVersion, &snapshot.PendingEvents, &snapshot.ProcessingErrors)
	if errors.Is(err, pgx.ErrNoRows) {
		return snapshot, ErrNotFound
	}
	return snapshot, err
}
