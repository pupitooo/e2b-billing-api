package billing

import (
	"context"
	"errors"
	"time"

	"e2b/billing-api/internal/accounting"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type AddonPurchase struct {
	SubscriptionID string    `json:"subscription_id"`
	AddonName      string    `json:"addon_name"`
	PurchasedAt    time.Time `json:"purchased_at"`
}

type Subscription struct {
	SubscriptionID    string    `json:"subscription_id"`
	CustomerID        string    `json:"customer_id"`
	AddonName         string    `json:"addon_name"`
	MonthlyPriceCents int64     `json:"monthly_price_cents"`
	PurchasedAt       time.Time `json:"purchased_at"`
	StartMonth        string    `json:"start_month"`
}

// PurchaseAddon snapshots the catalog price once. Replaying that identity
// returns the original subscription, including after its invoice is issued.
func (s *Store) PurchaseAddon(ctx context.Context, customer string, purchase AddonPurchase) (Subscription, error) {
	if err := validatePurchase(customer, purchase); err != nil {
		return Subscription{}, err
	}

	var result Subscription
	err := s.transact(ctx, func(tx pgx.Tx) error {
		if _, err := lockAccount(ctx, tx, customer); err != nil {
			return err
		}

		existing, err := subscriptionByID(ctx, tx, purchase.SubscriptionID)
		if err == nil {
			if existing.CustomerID != customer || existing.AddonName != purchase.AddonName || !existing.PurchasedAt.Equal(purchase.PurchasedAt) {
				return ErrConflict
			}

			result = existing

			return nil
		}

		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		return insertSubscription(ctx, tx, customer, purchase, &result)
	})

	return result, err
}

func validatePurchase(customer string, p AddonPurchase) error {
	if err := ValidateIdentifier("customer_id", customer); err != nil {
		return err
	}

	if err := ValidateIdentifier("subscription_id", p.SubscriptionID); err != nil {
		return err
	}

	if err := ValidateIdentifier("addon_name", p.AddonName); err != nil {
		return err
	}

	return validateTime("purchased_at", p.PurchasedAt)
}

func subscriptionByID(ctx context.Context, tx pgx.Tx, id string) (Subscription, error) {
	var value Subscription
	err := tx.QueryRow(ctx, `SELECT subscription_id,customer_id,addon_name,monthly_price_cents,
        purchased_at,start_month::text FROM addon_subscriptions WHERE subscription_id=$1`, id).
		Scan(&value.SubscriptionID, &value.CustomerID, &value.AddonName, &value.MonthlyPriceCents, &value.PurchasedAt, &value.StartMonth)

	return value, err
}

func insertSubscription(ctx context.Context, tx pgx.Tx, customer string, p AddonPurchase, result *Subscription) error {
	month, _ := accounting.UTCMonth(p.PurchasedAt)
	var closed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM closed_billing_months
		WHERE customer_id=$1 AND billing_month>=$2) OR EXISTS
		(SELECT 1 FROM invoice_closings WHERE customer_id=$1 AND billing_month>=$2)`, customer, month).Scan(&closed); err != nil {
		return err
	}

	if closed {
		return ErrConflict
	}

	var price int64
	err := tx.QueryRow(ctx, "SELECT monthly_price_cents FROM addons WHERE addon_name=$1", p.AddonName).Scan(&price)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}

	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `INSERT INTO addon_subscriptions VALUES ($1,$2,$3,$4,$5,$6)`,
		p.SubscriptionID, customer, p.AddonName, price, p.PurchasedAt.UTC(), month)
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) && databaseError.Code == "23505" {
		return ErrConflict
	}

	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, "UPDATE customer_billing_state SET state_version=state_version+1 WHERE customer_id=$1", customer)
	*result = Subscription{SubscriptionID: p.SubscriptionID, CustomerID: customer, AddonName: p.AddonName, MonthlyPriceCents: price, PurchasedAt: p.PurchasedAt.UTC(), StartMonth: month.Format("2006-01-02")}

	return err
}
