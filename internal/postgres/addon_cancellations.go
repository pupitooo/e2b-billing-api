package postgres

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"e2b/billing-api/internal/accounting"
	"e2b/billing-api/internal/billing"
	"github.com/jackc/pgx/v5"
)

const addonCancellationScopePrefix = "addon-cancellations/"

// Two escaped 256-byte components plus prefix, separator and a purchase-date suffix.
const maxSubscriptionIdentityBytes = 1_561

// CancelAddon appends an immutable end to one purchase period. Its UTC month is
// charged in full; later months omit that purchase. Replays preserve the result.
func (s *Store) CancelAddon(ctx context.Context, customer, key string, input billing.AddonCancellation) (billing.Cancellation, error) {
	if err := billing.ValidateIdentifier("customer_id", customer); err != nil {
		return billing.Cancellation{}, err
	}

	if err := billing.ValidateIdempotencyKey(key); err != nil {
		return billing.Cancellation{}, err
	}

	if strings.TrimSpace(input.SubscriptionID) == "" || strings.ContainsRune(input.SubscriptionID, 0) || !utf8.ValidString(input.SubscriptionID) || len(input.SubscriptionID) > maxSubscriptionIdentityBytes {
		return billing.Cancellation{}, &billing.ValidationError{Field: "subscription_id", Message: "Use a nonblank subscription identity without NUL, at most 1561 UTF-8 bytes."}
	}

	if err := billing.ValidateDate("cancelled_at", input.CancelledAt); err != nil {
		return billing.Cancellation{}, err
	}

	input.CancelledAt = input.CancelledAt.UTC()
	scope := addonCancellationScopePrefix + customer
	var result billing.Cancellation
	err := s.transact(ctx, func(tx pgx.Tx) error {
		found, err := operationResponse(ctx, tx, scope, key, &result)
		if err != nil {
			return err
		}

		if found {
			if result.SubscriptionID != input.SubscriptionID || !result.CancelledAt.Equal(input.CancelledAt) {
				return billing.ErrConflict
			}

			return nil
		}

		if _, err := lockAccount(ctx, tx, customer); err != nil {
			return err
		}

		result, err = cancelSubscription(ctx, tx, customer, input)
		if err != nil {
			return err
		}

		return recordOperation(ctx, tx, scope, key, input, result, s.now())
	})

	return result, err
}

// cancelSubscription validates the owning purchase and closure boundary under
// the account lock, then records termination and visibility in one transaction.
func cancelSubscription(ctx context.Context, tx pgx.Tx, customer string, input billing.AddonCancellation) (billing.Cancellation, error) {
	var purchased time.Time
	err := tx.QueryRow(ctx, "SELECT purchased_at FROM addon_subscriptions WHERE customer_id=$1 AND subscription_id=$2", customer, input.SubscriptionID).Scan(&purchased)
	if errors.Is(err, pgx.ErrNoRows) {
		return billing.Cancellation{}, billing.ErrNotFound
	}

	if err != nil {
		return billing.Cancellation{}, err
	}

	if input.CancelledAt.Before(purchased) {
		return billing.Cancellation{}, &billing.ValidationError{Field: "cancelled_at", Message: "Cancellation cannot precede purchase."}
	}

	month, _ := accounting.UTCMonth(input.CancelledAt)
	var conflict bool
	err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM addon_cancellations WHERE subscription_id=$1)
  OR EXISTS (SELECT 1 FROM closed_billing_months WHERE customer_id=$2 AND billing_month>=$3)`, input.SubscriptionID, customer, month).Scan(&conflict)
	if err != nil {
		return billing.Cancellation{}, err
	}

	if conflict {
		return billing.Cancellation{}, billing.ErrConflict
	}

	if _, err := tx.Exec(ctx, "INSERT INTO addon_cancellations VALUES ($1,$2,$3)", input.SubscriptionID, input.CancelledAt, month); err != nil {
		return billing.Cancellation{}, err
	}

	_, err = tx.Exec(ctx, "UPDATE customer_billing_state SET state_version=state_version+1 WHERE customer_id=$1", customer)
	result := billing.Cancellation{CustomerID: customer, SubscriptionID: input.SubscriptionID, CancelledAt: input.CancelledAt, EndMonth: month.Format(time.DateOnly)}

	return result, err
}
