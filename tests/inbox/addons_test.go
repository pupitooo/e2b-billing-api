//go:build integration

package inbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"e2b/billing-api/internal/billing"
)

// TestPurchaseAddon snapshots full monthly prices and preserves retry identity;
// separate cases explicitly declare conflicts, closure state, and final count.
func TestPurchaseAddon(t *testing.T) {
	t.Run("replay after catalog change and closing", testAddonReplayAfterClosing)
	t.Run("stored IDs survive readable format", testStoredAddonIdentities)
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
			purchase := billing.AddonPurchase{AddonName: tc.addon, PurchasedAt: parseBillingTime(t, tc.purchased)}
			actual, err := store.PurchaseAddon(ctx, tc.customer, "purchase", purchase)
			if err == nil && tc.repeatID != "" {
				_, err = store.PurchaseAddon(ctx, tc.customer, tc.repeatID, purchase)
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
	t.Run("readable subscription IDs", testReadableAddonIdentities)
	t.Run("resource idempotency", func(t *testing.T) {
		testResourceIdempotency(t, resourceEndpoint{
			path: "/customers/acme/addons", idField: "subscription_id",
			body:        `{"addon_name":"concurrency_pack","purchased_at":"2026-10-05T00:00:00Z"}`,
			changedBody: `{"addon_name":"concurrency_pack","purchased_at":"2026-10-06T00:00:00Z"}`,
			legacyBody:  `{"subscription_id":"caller-id","addon_name":"concurrency_pack","purchased_at":"2026-10-05T00:00:00Z"}`,
			wantPrices:  3, wantSubscriptions: 1, wantVersion: 1,
		})
	})
	cases := []struct {
		name       string
		body       string
		key        string
		wantStatus int
		wantBody   string
	}{
		{key: "acme-pack", name: "assignment purchase with customer and addon identity", body: `{"addon_name":"concurrency_pack","purchased_at":"2026-10-05T00:00:00Z"}`, wantStatus: 200, wantBody: `{"subscription_id":"subscription/acme/concurrency_pack","customer_id":"acme","addon_name":"concurrency_pack","monthly_price_cents":2000,"purchased_at":"2026-10-05T00:00:00Z","start_month":"2026-10-01"}`},
		{key: "acme-pack", name: "missing purchase time", body: `{"addon_name":"concurrency_pack"}`, wantStatus: 422},
		{key: "acme-pack", name: "missing add-on", body: `{"addon_name":"unknown","purchased_at":"2026-10-05T00:00:00Z"}`, wantStatus: 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := financialRequest(t, financialHandler(t), "POST", "/customers/acme/addons", tc.body, tc.key)
			if response.Code != tc.wantStatus {
				t.Fatalf("POST addons(%s): status=%d body=%s want %d", tc.body, response.Code, response.Body, tc.wantStatus)
			}
			var actual map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
				t.Fatal(err)
			}
			if response.Code == 200 {
				id, ok := actual["subscription_id"].(string)
				if !ok || id == "" || id == tc.key {
					t.Fatalf("POST addons: generated subscription_id=%v; want nonempty ID distinct from key %q", actual["subscription_id"], tc.key)
				}
			}
			expected := map[string]any{}
			if tc.wantBody != "" {
				if err := json.Unmarshal([]byte(tc.wantBody), &expected); err != nil {
					t.Fatal(err)
				}
			}
			if tc.wantBody != "" && !reflect.DeepEqual(actual, expected) {
				t.Fatalf("POST addons: body=%s want %s", response.Body, tc.wantBody)
			}
		})
	}
}
