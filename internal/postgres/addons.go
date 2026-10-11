package postgres

import (
	"context"
	"errors"
	"net/url"

	"e2b/billing-api/internal/accounting"
	"e2b/billing-api/internal/billing"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	addonOperationScopePrefix = "addons/"
	subscriptionIDPrefix      = "subscription/"
)

// PurchaseAddon snapshots the catalog price once. Replaying the scoped request key
// returns the original subscription, including after its invoice is issued.
func (s *Store) PurchaseAddon(ctx context.Context, customer, key string, purchase billing.AddonPurchase) (billing.Subscription, error) {
	if err := billing.ValidateIdempotencyKey(key); err != nil {
		return billing.Subscription{}, err
	}

	if err := validatePurchase(customer, purchase); err != nil {
		return billing.Subscription{}, err
	}

	purchase.PurchasedAt = purchase.PurchasedAt.UTC()
	scope := addonOperationScopePrefix + customer
	var result billing.Subscription
	err := s.transact(ctx, func(tx pgx.Tx) error {
		found, err := operationResponse(ctx, tx, scope, key, &result)
		if err != nil {
			return err
		}

		if found {
			if result.CustomerID != customer || result.AddonName != purchase.AddonName || !result.PurchasedAt.Equal(purchase.PurchasedAt) {
				return billing.ErrConflict
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

func validatePurchase(customer string, p billing.AddonPurchase) error {
	if err := billing.ValidateIdentifier("customer_id", customer); err != nil {
		return err
	}

	if err := billing.ValidateIdentifier("addon_name", p.AddonName); err != nil {
		return err
	}

	return billing.ValidateDate("purchased_at", p.PurchasedAt)
}

func insertSubscription(ctx context.Context, tx pgx.Tx, customer, subscriptionID string, p billing.AddonPurchase, result *billing.Subscription) error {
	month, _ := accounting.UTCMonth(p.PurchasedAt)
	var closed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM closed_billing_months
		WHERE customer_id=$1 AND billing_month>=$2)`, customer, month).Scan(&closed); err != nil {
		return err
	}

	if closed {
		return billing.ErrConflict
	}

	var price int64
	err := tx.QueryRow(ctx, "SELECT monthly_price_cents FROM addons WHERE addon_name=$1", p.AddonName).Scan(&price)
	if errors.Is(err, pgx.ErrNoRows) {
		return billing.ErrNotFound
	}

	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `INSERT INTO addon_subscriptions VALUES ($1,$2,$3,$4,$5,$6)`,
		subscriptionID, customer, p.AddonName, price, p.PurchasedAt.UTC(), month)
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) && databaseError.Code == "23505" {
		return billing.ErrConflict
	}

	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, "UPDATE customer_billing_state SET state_version=state_version+1 WHERE customer_id=$1", customer)
	*result = billing.Subscription{SubscriptionID: subscriptionID, CustomerID: customer, AddonName: p.AddonName, MonthlyPriceCents: price, PurchasedAt: p.PurchasedAt.UTC(), StartMonth: month.Format("2006-01-02")}

	return err
}
