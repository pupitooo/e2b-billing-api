//go:build integration

package inbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
)

// testReadableAddonIdentities verifies escaped IDs through HTTP, persistence,
// retries, and invoices. Each boundary case uses an isolated seeded schema;
// rejected identifiers leave no subscription, operation, or account change.
func testReadableAddonIdentities(t *testing.T) {
	cases := []struct {
		name             string
		customer         string
		addon            string
		catalogPrice     int64
		wantStatus       int
		wantID           string
		wantField        string
		wantState        resourceState
		wantInvoiceCents int64
		wantInvoiceLines int
	}{
		{
			name: "separator in customer", customer: "acme/ops", addon: "concurrency_pack", catalogPrice: 2000,
			wantStatus: 200, wantID: "subscription/acme%2Fops/concurrency_pack", wantInvoiceCents: 2000, wantInvoiceLines: 2,
			wantState: resourceState{prices: 3, subscriptions: 1, operations: 1, stateVersion: 1, creditTicks: "0"},
		},
		{
			name: "separator in addon", customer: "acme", addon: "ops/concurrency_pack", catalogPrice: 2000,
			wantStatus: 200, wantID: "subscription/acme/ops%2Fconcurrency_pack", wantInvoiceCents: 2000, wantInvoiceLines: 2,
			wantState: resourceState{prices: 3, subscriptions: 1, operations: 1, stateVersion: 1, creditTicks: "0"},
		},
		{
			name: "literal escape remains distinct", customer: "acme%2Fops", addon: "concurrency_pack", catalogPrice: 2000,
			wantStatus: 200, wantID: "subscription/acme%252Fops/concurrency_pack", wantInvoiceCents: 2000, wantInvoiceLines: 2,
			wantState: resourceState{prices: 3, subscriptions: 1, operations: 1, stateVersion: 1, creditTicks: "0"},
		},
		{
			name: "Unicode identifiers", customer: "客户", addon: "測定", catalogPrice: 2000,
			wantStatus: 200, wantID: "subscription/%E5%AE%A2%E6%88%B7/%E6%B8%AC%E5%AE%9A", wantInvoiceCents: 2000, wantInvoiceLines: 2,
			wantState: resourceState{prices: 3, subscriptions: 1, operations: 1, stateVersion: 1, creditTicks: "0"},
		},
		{
			name: "maximum escaped input lengths", customer: strings.Repeat("/", 256), addon: strings.Repeat("/", 256), catalogPrice: 2000,
			wantStatus: 200, wantID: "subscription/" + strings.Repeat("%2F", 256) + "/" + strings.Repeat("%2F", 256), wantInvoiceCents: 2000, wantInvoiceLines: 2,
			wantState: resourceState{prices: 3, subscriptions: 1, operations: 1, stateVersion: 1, creditTicks: "0"},
		},
		{
			name: "customer above byte limit", customer: strings.Repeat("/", 257), addon: "concurrency_pack", catalogPrice: 2000,
			wantStatus: 422, wantField: "customer_id", wantState: resourceState{prices: 3, creditTicks: "0"},
		},
		{
			name: "addon above byte limit", customer: "acme", addon: strings.Repeat("/", 257), catalogPrice: 2000,
			wantStatus: 422, wantField: "addon_name", wantState: resourceState{prices: 3, creditTicks: "0"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			if _, err := pool.Exec(ctx, "INSERT INTO customers VALUES ($1,'Identity test','CZ','Test address') ON CONFLICT DO NOTHING", tc.customer); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "INSERT INTO customer_billing_state (customer_id,credit_balance_ticks,spend_limit_cents) VALUES ($1,0,NULL) ON CONFLICT DO NOTHING", tc.customer); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "INSERT INTO addons VALUES ($1,$2) ON CONFLICT DO NOTHING", tc.addon, tc.catalogPrice); err != nil {
				t.Fatal(err)
			}
			input := struct {
				IdempotencyKey string `json:"idempotency_key"`
				AddonName      string `json:"addon_name"`
				PurchasedAt    string `json:"purchased_at"`
			}{IdempotencyKey: "readable-purchase", AddonName: tc.addon, PurchasedAt: "2026-10-05T00:00:00Z"}
			body, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			path := "/customers/" + url.PathEscape(tc.customer) + "/addons"

			response := financialRequest(t, resourceHandler(pool), http.MethodPost, path, string(body))

			if response.Code != tc.wantStatus {
				t.Fatalf("POST %s addon=%q: HTTP=%d body=%s; want %d", path, tc.addon, response.Code, response.Body, tc.wantStatus)
			}
			assertResourceState(t, pool, tc.wantState, tc.customer)
			if tc.wantField != "" {
				var actual struct {
					Error struct {
						Field string `json:"field"`
					} `json:"error"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
					t.Fatal(err)
				}
				if actual.Error.Field != tc.wantField {
					t.Fatalf("POST %s addon=%q: error field=%q; want %q", path, tc.addon, actual.Error.Field, tc.wantField)
				}

				return
			}

			var actual billing.Subscription
			if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
				t.Fatal(err)
			}
			if actual.SubscriptionID != tc.wantID {
				t.Fatalf("POST %s addon=%q: subscription_id=%q; want %q", path, tc.addon, actual.SubscriptionID, tc.wantID)
			}
			var storedID string
			if err := pool.QueryRow(ctx, "SELECT subscription_id FROM addon_subscriptions WHERE customer_id=$1 AND addon_name=$2", tc.customer, tc.addon).Scan(&storedID); err != nil {
				t.Fatal(err)
			}
			if storedID != tc.wantID {
				t.Fatalf("Stored subscription(customer=%q addon=%q)=%q; want %q", tc.customer, tc.addon, storedID, tc.wantID)
			}

			replay := financialRequest(t, resourceHandler(pool), http.MethodPost, path, string(body))

			if replay.Code != tc.wantStatus || replay.Body.String() != response.Body.String() {
				t.Fatalf("Replay POST %s: HTTP=%d body=%s; want HTTP=%d body=%s", path, replay.Code, replay.Body, tc.wantStatus, response.Body)
			}
			assertResourceState(t, pool, tc.wantState, tc.customer)

			store := billing.NewStoreWithClock(pool, func() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) })
			invoice, err := store.CloseMonth(ctx, tc.customer, "2026-10")
			if err != nil {
				t.Fatalf("CloseMonth(customer=%q): %v", tc.customer, err)
			}
			if invoice.TotalCents != tc.wantInvoiceCents || len(invoice.Lines) != tc.wantInvoiceLines || invoice.Lines[0].SubscriptionID != tc.wantID {
				t.Fatalf("Invoice(customer=%q): total=%d lines=%+v; want total=%d lines=%d and first subscription_id=%q", tc.customer, invoice.TotalCents, invoice.Lines, tc.wantInvoiceCents, tc.wantInvoiceLines, tc.wantID)
			}
		})
	}
}

// testStoredAddonIdentities reconstructs earlier committed subscriptions and
// operation results. Replays preserve their IDs and issued invoice snapshots;
// a different key cannot replace the existing customer/add-on subscription.
func testStoredAddonIdentities(t *testing.T) {
	cases := []struct {
		name             string
		storedID         string
		wantID           string
		wantInvoiceCents int64
		wantInvoiceLines int
		wantConflict     error
		wantState        resourceState
	}{
		{
			name: "earlier caller ID", storedID: "acme-pack", wantID: "acme-pack", wantInvoiceCents: 2000, wantInvoiceLines: 2, wantConflict: billing.ErrConflict,
			wantState: resourceState{prices: 3, subscriptions: 1, operations: 1, stateVersion: 2, creditTicks: "0"},
		},
		{
			name: "earlier generated hash", storedID: "subscription/fb825258fbc40ba90cc30768910b4290b44251059999cba11a7a6c6efc2c5343",
			wantID: "subscription/fb825258fbc40ba90cc30768910b4290b44251059999cba11a7a6c6efc2c5343", wantInvoiceCents: 2000, wantInvoiceLines: 2, wantConflict: billing.ErrConflict,
			wantState: resourceState{prices: 3, subscriptions: 1, operations: 1, stateVersion: 2, creditTicks: "0"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			original := billing.Subscription{
				SubscriptionID: tc.storedID, CustomerID: "acme", AddonName: "concurrency_pack", MonthlyPriceCents: 2000,
				PurchasedAt: parseBillingTime(t, "2026-10-05T00:00:00Z"), StartMonth: "2026-10-01",
			}
			encoded, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "INSERT INTO addon_subscriptions VALUES ($1,$2,$3,$4,$5,$6)",
				original.SubscriptionID, original.CustomerID, original.AddonName, original.MonthlyPriceCents, original.PurchasedAt, original.StartMonth); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO api_idempotency_operations VALUES
				('addons/acme','earlier-purchase','{"addon_name":"concurrency_pack","purchased_at":"2026-10-05T00:00:00Z"}',$1,'2026-10-05T00:00:00Z')`, encoded); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "UPDATE customer_billing_state SET state_version=1 WHERE customer_id='acme'"); err != nil {
				t.Fatal(err)
			}
			store := billing.NewStoreWithClock(pool, func() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) })
			invoice, err := store.CloseMonth(ctx, original.CustomerID, "2026-10")
			if err != nil {
				t.Fatal(err)
			}
			if invoice.TotalCents != tc.wantInvoiceCents || len(invoice.Lines) != tc.wantInvoiceLines || invoice.Lines[0].SubscriptionID != tc.wantID {
				t.Fatalf("Stored invoice=%+v; want total=%d lines=%d and first subscription_id=%q", invoice, tc.wantInvoiceCents, tc.wantInvoiceLines, tc.wantID)
			}
			purchase := billing.AddonPurchase{AddonName: original.AddonName, PurchasedAt: original.PurchasedAt}

			replay, err := billing.NewStore(pool).PurchaseAddon(ctx, original.CustomerID, "earlier-purchase", purchase)

			if err != nil {
				t.Fatalf("Replay stored subscription_id=%q: %v", tc.storedID, err)
			}
			if replay != original || replay.SubscriptionID != tc.wantID {
				t.Fatalf("Replay=%+v; want original=%+v with subscription_id=%q", replay, original, tc.wantID)
			}

			_, err = store.PurchaseAddon(ctx, original.CustomerID, "another-purchase", purchase)

			if !errors.Is(err, tc.wantConflict) {
				t.Fatalf("New key for stored subscription_id=%q: error=%v; want %v", tc.storedID, err, tc.wantConflict)
			}
			frozen, err := store.Invoice(ctx, original.CustomerID, "2026-10")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(frozen, invoice) {
				t.Fatalf("Invoice after replay=%+v; want original=%+v", frozen, invoice)
			}
			assertResourceState(t, pool, tc.wantState)
		})
	}
}
