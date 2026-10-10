package billing

import (
	"context"
	"errors"
	"net/url"
	"time"

	"e2b/billing-api/internal/accounting"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	addonOperationScopePrefix = "addons/"
	subscriptionIDPrefix      = "subscription/"
)

type AddonPurchase struct {
	AddonName   string    `json:"addon_name"`
	PurchasedAt time.Time `json:"purchased_at"`
}

type Subscription struct {
	SubscriptionID    string    `json:"subscription_id"`
	CustomerID        string    `json:"customer_id"`
	AddonName         string    `json:"addon_name"`
	MonthlyPriceCents int64     `json:"monthly_price_cents"`
	PurchasedAt       time.Time `json:"purchased_at"`
	StartMonth        string    `json:"start_month"`
}

// PurchaseAddon snapshots the catalog price once. Replaying the scoped request key
// returns the original subscription, including after its invoice is issued.
func (s *Store) PurchaseAddon(ctx context.Context, customer, key string, purchase AddonPurchase) (Subscription, error) {
	if err := ValidateIdempotencyKey(key); err != nil {
		return Subscription{}, err
	}

	if err := validatePurchase(customer, purchase); err != nil {
		return Subscription{}, err
	}

	purchase.PurchasedAt = purchase.PurchasedAt.UTC()
	scope := addonOperationScopePrefix + customer
	var result Subscription
	err := s.transact(ctx, func(tx pgx.Tx) error {
		found, err := operationResponse(ctx, tx, scope, key, &result)
		if err != nil {
			return err
		}

		if found {
			if result.CustomerID != customer || result.AddonName != purchase.AddonName || !result.PurchasedAt.Equal(purchase.PurchasedAt) {
				return ErrConflict
			}

			return nil
		}

		if _, err := lockAccount(ctx, tx, customer); err != nil {
			return err
		}

		subscriptionID := subscriptionIdentity(customer, purchase.AddonName)
		if err := insertSubscription(ctx, tx, customer, subscriptionID, purchase, &result); err != nil {
			return err
		}

		return recordOperation(ctx, tx, scope, key, purchase, result, s.now())
	})

	return result, err
}

// subscriptionIdentity preserves the readable customer/add-on tuple. Escaping
// each path segment keeps separators, percent signs, and Unicode unambiguous.
func subscriptionIdentity(customer, addon string) string {
	return subscriptionIDPrefix + url.PathEscape(customer) + "/" + url.PathEscape(addon)
}

func validatePurchase(customer string, p AddonPurchase) error {
	if err := ValidateIdentifier("customer_id", customer); err != nil {
		return err
	}

	if err := ValidateIdentifier("addon_name", p.AddonName); err != nil {
		return err
	}

	return validateTime("purchased_at", p.PurchasedAt)
}

func insertSubscription(ctx context.Context, tx pgx.Tx, customer, subscriptionID string, p AddonPurchase, result *Subscription) error {
	month, _ := accounting.UTCMonth(p.PurchasedAt)
	var closed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM closed_billing_months
		WHERE customer_id=$1 AND billing_month>=$2)`, customer, month).Scan(&closed); err != nil {
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
		subscriptionID, customer, p.AddonName, price, p.PurchasedAt.UTC(), month)
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) && databaseError.Code == "23505" {
		return ErrConflict
	}

	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, "UPDATE customer_billing_state SET state_version=state_version+1 WHERE customer_id=$1", customer)
	*result = Subscription{SubscriptionID: subscriptionID, CustomerID: customer, AddonName: p.AddonName, MonthlyPriceCents: price, PurchasedAt: p.PurchasedAt.UTC(), StartMonth: month.Format("2006-01-02")}

	return err
}
