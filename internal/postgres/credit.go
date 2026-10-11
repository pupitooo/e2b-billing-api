package postgres

import (
	"context"
	"errors"
	"time"

	"e2b/billing-api/internal/accounting"
	"e2b/billing-api/internal/billing"
	"github.com/jackc/pgx/v5"
)

const creditOperationScopePrefix = "credits/"

var errCreditAuditClock = errors.New("credit audit clock is outside the supported timestamp range")

type creditGrantResult struct {
	CreditEntryID string              `json:"credit_entry_id"`
	Grant         billing.CreditGrant `json:"grant"`
}

// GrantCredit records the API key separately from the server-generated ledger
// identity. Replay leaves both balance and earlier usage allocations unchanged.
func (s *Store) GrantCredit(ctx context.Context, customer string, grant billing.CreditGrant) error {
	if err := billing.ValidateIdentifier("customer_id", customer); err != nil {
		return err
	}

	if err := billing.ValidateIdempotencyKey(grant.IdempotencyKey); err != nil {
		return err
	}

	if err := billing.ValidateCents("amount_cents", grant.AmountCents, true); err != nil {
		return err
	}

	grantAmount, _ := accounting.FromCents(grant.AmountCents)
	scope := creditOperationScopePrefix + customer

	return s.transact(ctx, func(tx pgx.Tx) error {
		var previous creditGrantResult
		found, err := operationResponse(ctx, tx, scope, grant.IdempotencyKey, &previous)
		if err != nil {
			return err
		}

		if found {
			if previous.Grant.AmountCents == grant.AmountCents {
				return nil
			}

			return billing.ErrConflict
		}

		if _, err := lockAccount(ctx, tx, customer); err != nil {
			return err
		}

		recordedAt := s.now().UTC().Truncate(time.Microsecond)
		if err := billing.ValidateTime("recorded_at", recordedAt); err != nil {
			// An invalid backend clock is a server failure, not a caller field error.
			return errCreditAuditClock
		}

		entryID, err := nextAccountingID(ctx, tx, creditEntryIDSequence, creditEntryIDPrefix)
		if err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `INSERT INTO credit_entries
            (credit_entry_id,customer_id,group_id,amount_microcents,recorded_at)
            VALUES ($1,$2,NULL,$3::numeric,$4)`, entryID,
			customer, grantAmount.Microcents().String(), recordedAt)
		if err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `UPDATE customer_billing_state SET credit_balance_microcents=credit_balance_microcents+$2::numeric,
            state_version=state_version+1 WHERE customer_id=$1`, customer, grantAmount.Microcents().String())
		if err != nil {
			return err
		}

		result := creditGrantResult{CreditEntryID: entryID, Grant: grant}

		return recordOperation(ctx, tx, scope, grant.IdempotencyKey, grant, result, recordedAt)
	})
}

func (s *Store) Credit(ctx context.Context, customer string) (billing.CreditSnapshot, error) {
	if err := billing.ValidateIdentifier("customer_id", customer); err != nil {
		return billing.CreditSnapshot{}, err
	}

	var snapshot billing.CreditSnapshot
	err := s.pool.QueryRow(ctx, `SELECT customer_id,credit_balance_microcents::text,state_version,
        (SELECT count(*) FROM usage_inbox WHERE customer_id=$1 AND processed_at IS NULL AND processing_error IS NULL),
        (SELECT count(*) FROM usage_inbox WHERE customer_id=$1 AND processing_error IS NOT NULL)
        FROM customer_billing_state WHERE customer_id=$1`, customer).
		Scan(&snapshot.CustomerID, &snapshot.BalanceMicrocents, &snapshot.StateVersion, &snapshot.PendingEvents, &snapshot.ProcessingErrors)
	if errors.Is(err, pgx.ErrNoRows) {
		return snapshot, billing.ErrNotFound
	}

	return snapshot, err
}
