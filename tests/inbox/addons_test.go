//go:build integration

package inbox_test

import (
	"context"
	"errors"
	"testing"

	"e2b/billing-api/internal/billing"
)

// TestPurchaseAddon snapshots full monthly prices and preserves retry identity;
// separate cases explicitly declare conflicts, closure state, and final count.
func TestPurchaseAddon(t *testing.T) {
	cases := []struct {
		name      string
		customer  string
		addon     string
		purchased string
		repeatID  string
		closed    string
		wantError error
		wantCount int
		wantPrice int64
		wantMonth string
	}{
		{name: "month-end purchase and identical retry", customer: "acme", addon: "concurrency_pack", purchased: "2026-10-31T23:59:59Z", repeatID: "purchase", wantCount: 1, wantPrice: 2000, wantMonth: "2026-10-01"},
		{name: "second subscription identity conflicts", customer: "acme", addon: "concurrency_pack", purchased: "2026-10-05T00:00:00Z", repeatID: "other", wantError: billing.ErrConflict, wantCount: 1, wantPrice: 2000, wantMonth: "2026-10-01"},
		{name: "closed month cannot acquire retroactive add-on", customer: "acme", addon: "concurrency_pack", purchased: "2026-10-05T00:00:00Z", closed: "2026-10-01", wantError: billing.ErrConflict, wantCount: 0},
		{name: "unknown catalog add-on", customer: "acme", addon: "missing", purchased: "2026-10-05T00:00:00Z", wantError: billing.ErrNotFound, wantCount: 0},
		{name: "unknown customer", customer: "missing", addon: "concurrency_pack", purchased: "2026-10-05T00:00:00Z", wantError: billing.ErrNotFound, wantCount: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			store := billing.NewStore(pool)
			ctx := context.Background()
			if tc.closed != "" {
				if _, err := pool.Exec(ctx, "INSERT INTO closed_billing_months VALUES ($1,$2,$3)", tc.customer, tc.closed, parseBillingTime(t, "2026-11-01T00:00:00Z")); err != nil {
					t.Fatal(err)
				}
			}
			purchase := billing.AddonPurchase{SubscriptionID: "purchase", AddonName: tc.addon, PurchasedAt: parseBillingTime(t, tc.purchased)}
			actual, err := store.PurchaseAddon(ctx, tc.customer, purchase)
			if err == nil && tc.repeatID != "" {
				purchase.SubscriptionID = tc.repeatID
				_, err = store.PurchaseAddon(ctx, tc.customer, purchase)
			}
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("PurchaseAddon(customer=%s addon=%s date=%s repeat=%s): error=%v want %v", tc.customer, tc.addon, tc.purchased, tc.repeatID, err, tc.wantError)
			}
			var count int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM addon_subscriptions").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != tc.wantCount {
				t.Fatalf("subscriptions=%d want %d", count, tc.wantCount)
			}
			if tc.wantCount > 0 && (actual.MonthlyPriceCents != tc.wantPrice || actual.StartMonth != tc.wantMonth) {
				t.Fatalf("subscription=%+v want price=%d month=%s", actual, tc.wantPrice, tc.wantMonth)
			}
		})
	}
}

// TestAddonAPI verifies the public purchase response, including the price and
// UTC activation month; validation cases cannot create a subscription.
func TestAddonAPI(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{name: "assignment purchase", body: `{"subscription_id":"acme-pack","addon_name":"concurrency_pack","purchased_at":"2026-10-05T00:00:00Z"}`, wantStatus: 200, wantBody: `{"subscription_id":"acme-pack","customer_id":"acme","addon_name":"concurrency_pack","monthly_price_cents":2000,"purchased_at":"2026-10-05T00:00:00Z","start_month":"2026-10-01"}`},
		{name: "missing purchase time", body: `{"subscription_id":"acme-pack","addon_name":"concurrency_pack"}`, wantStatus: 422},
		{name: "missing add-on", body: `{"subscription_id":"acme-pack","addon_name":"unknown","purchased_at":"2026-10-05T00:00:00Z"}`, wantStatus: 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := financialRequest(t, financialHandler(t), "POST", "/customers/acme/addons", tc.body)
			if response.Code != tc.wantStatus {
				t.Fatalf("POST addons(%s): status=%d body=%s want %d", tc.body, response.Code, response.Body, tc.wantStatus)
			}
			if tc.wantBody != "" && response.Body.String() != tc.wantBody+"\n" {
				t.Fatalf("POST addons: body=%s want %s", response.Body, tc.wantBody)
			}
		})
	}
}
