//go:build integration

package inbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/postgres"
)

// TestCancelAddon preserves complete months and append-only purchase history;
// invalid dates, ownership, closed periods and changed retries have no effects.
func TestCancelAddon(t *testing.T) {
	t.Run("concurrent cancellation identities", testCancellationConcurrency)
	t.Run("cancellation and invoice lock order", testCancellationClosingOrder)
	cases := []struct {
		name, customer, subscription, purchased, cancelled, closed string
		repeatKey, repeatDate                                      string
		wantError                                                  error
		wantField                                                  string
		wantCancellations                                          int
		wantVersion                                                int64
		wantEndMonth                                               string
	}{
		{name: "purchase day still pays full month", customer: "acme", subscription: "subscription/acme/concurrency_pack", purchased: "2026-10-01T00:00:00Z", cancelled: "2026-10-01T00:00:00Z", wantCancellations: 1, wantVersion: 2, wantEndMonth: "2026-10-01"},
		{name: "last day retains purchase month", customer: "acme", subscription: "subscription/acme/concurrency_pack", purchased: "2026-10-01T00:00:00Z", cancelled: "2026-10-31T00:00:00Z", wantCancellations: 1, wantVersion: 2, wantEndMonth: "2026-10-01"},
		{name: "first next-month day also charges next month", customer: "acme", subscription: "subscription/acme/concurrency_pack", purchased: "2026-10-01T00:00:00Z", cancelled: "2026-11-01T00:00:00Z", wantCancellations: 1, wantVersion: 2, wantEndMonth: "2026-11-01"},
		{name: "same key after restart retains one end record", customer: "acme", subscription: "subscription/acme/concurrency_pack", purchased: "2026-10-01T00:00:00Z", cancelled: "2026-10-31T00:00:00Z", repeatKey: "cancel", repeatDate: "2026-10-31T00:00:00Z", wantCancellations: 1, wantVersion: 2, wantEndMonth: "2026-10-01"},
		{name: "changed replay conflicts", customer: "acme", subscription: "subscription/acme/concurrency_pack", purchased: "2026-10-01T00:00:00Z", cancelled: "2026-10-31T00:00:00Z", repeatKey: "cancel", repeatDate: "2026-11-01T00:00:00Z", wantError: billing.ErrConflict, wantCancellations: 1, wantVersion: 2},
		{name: "new key cannot replace existing termination", customer: "acme", subscription: "subscription/acme/concurrency_pack", purchased: "2026-10-01T00:00:00Z", cancelled: "2026-10-31T00:00:00Z", repeatKey: "other", repeatDate: "2026-11-01T00:00:00Z", wantError: billing.ErrConflict, wantCancellations: 1, wantVersion: 2},
		{name: "closed cancellation month conflicts", customer: "acme", subscription: "subscription/acme/concurrency_pack", purchased: "2026-10-01T00:00:00Z", cancelled: "2026-10-31T00:00:00Z", closed: "2026-10", wantError: billing.ErrConflict, wantVersion: 2},
		{name: "first open month after closure permits cancellation", customer: "acme", subscription: "subscription/acme/concurrency_pack", purchased: "2026-10-01T00:00:00Z", cancelled: "2026-11-01T00:00:00Z", closed: "2026-10", wantCancellations: 1, wantVersion: 3, wantEndMonth: "2026-11-01"},
		{name: "day before purchase fails", customer: "acme", subscription: "subscription/acme/concurrency_pack", purchased: "2026-10-02T00:00:00Z", cancelled: "2026-10-01T00:00:00Z", wantField: "cancelled_at", wantVersion: 1},
		{name: "intraday timestamp fails domain date contract", customer: "acme", subscription: "subscription/acme/concurrency_pack", purchased: "2026-10-01T00:00:00Z", cancelled: "2026-10-01T00:00:00.000001Z", wantField: "cancelled_at", wantVersion: 1},
		{name: "another customer cannot cancel a purchase", customer: "cyberdyne", subscription: "subscription/acme/concurrency_pack", purchased: "2026-10-01T00:00:00Z", cancelled: "2026-10-31T00:00:00Z", wantError: billing.ErrNotFound, wantVersion: 1},
		{name: "largest cancellation identity remains a valid lookup", customer: "acme", subscription: strings.Repeat("x", 1_561), purchased: "2026-10-01T00:00:00Z", cancelled: "2026-10-31T00:00:00Z", wantError: billing.ErrNotFound, wantVersion: 1},
		{name: "first excessive cancellation identity fails", customer: "acme", subscription: strings.Repeat("x", 1_562), purchased: "2026-10-01T00:00:00Z", cancelled: "2026-10-31T00:00:00Z", wantField: "subscription_id", wantVersion: 1},
		{name: "missing subscription fails", customer: "acme", subscription: "missing", purchased: "2026-10-01T00:00:00Z", cancelled: "2026-10-31T00:00:00Z", wantError: billing.ErrNotFound, wantVersion: 1},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			pool := billingDatabase(t)
			config := postgres.Config{Now: func() time.Time { return parseBillingTime(t, "2027-02-01T00:00:00Z") }}
			store := newPostgresStore(t, pool, config)
			if _, err := store.PurchaseAddon(ctx, "acme", "purchase", billing.AddonPurchase{AddonName: "concurrency_pack", PurchasedAt: parseBillingTime(t, scenario.purchased)}); err != nil {
				t.Fatal(err)
			}
			if scenario.closed != "" {
				if _, err := store.CloseMonth(ctx, "acme", scenario.closed); err != nil {
					t.Fatal(err)
				}
			}
			input := billing.AddonCancellation{SubscriptionID: scenario.subscription, CancelledAt: parseBillingTime(t, scenario.cancelled)}
			before := addonFinancialSnapshot(t, pool)
			result, err := store.CancelAddon(ctx, scenario.customer, "cancel", input)
			if err == nil && scenario.repeatKey != "" {
				before = addonFinancialSnapshot(t, pool)
				input.CancelledAt = parseBillingTime(t, scenario.repeatDate)
				result, err = newPostgresStore(t, pool, config).CancelAddon(ctx, scenario.customer, scenario.repeatKey, input)
			}
			if scenario.wantField != "" {
				var validation *billing.ValidationError
				if !errors.As(err, &validation) || validation.Field != scenario.wantField {
					t.Fatalf("CancelAddon(%+v) error=%v; want field=%s", input, err, scenario.wantField)
				}
			} else if !errors.Is(err, scenario.wantError) {
				t.Fatalf("CancelAddon(%+v) error=%v; want %v", input, err, scenario.wantError)
			}
			if err != nil {
				if after := addonFinancialSnapshot(t, pool); after != before {
					t.Fatalf("rejected cancellation changed state: before=%s after=%s", before, after)
				}
			}
			var count int
			var version int64
			if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM addon_cancellations),state_version FROM customer_billing_state WHERE customer_id='acme'").Scan(&count, &version); err != nil {
				t.Fatal(err)
			}
			if count != scenario.wantCancellations || version != scenario.wantVersion {
				t.Fatalf("CancelAddon(%+v) cancellations=%d version=%d; want %d,%d", input, count, version, scenario.wantCancellations, scenario.wantVersion)
			}
			if scenario.wantEndMonth != "" && result.EndMonth != scenario.wantEndMonth {
				t.Fatalf("CancelAddon(%+v) result=%+v; want end=%s", input, result, scenario.wantEndMonth)
			}
		})
	}
}

// TestCancelAddonAPI checks explicit transport fields and date validation, then
// verifies the end-month response and absence of side effects on every rejection.
func TestCancelAddonAPI(t *testing.T) {
	cases := []struct {
		name, body          string
		wantStatus          int
		wantCode, wantField string
		wantResult          billing.Cancellation
	}{
		{name: "cancel full purchase month", body: `{"idempotency_key":"cancel","subscription_id":"subscription/acme/concurrency_pack","cancelled_at":"2026-10-31"}`, wantStatus: 200, wantResult: billing.Cancellation{CustomerID: "acme", SubscriptionID: "subscription/acme/concurrency_pack", EndMonth: "2026-10-01"}},
		{name: "missing key", body: `{"subscription_id":"subscription/acme/concurrency_pack","cancelled_at":"2026-10-31"}`, wantStatus: 422, wantCode: "invalid_command", wantField: "idempotency_key"},
		{name: "timestamp rejected", body: `{"idempotency_key":"cancel","subscription_id":"subscription/acme/concurrency_pack","cancelled_at":"2026-10-31T00:00:00Z"}`, wantStatus: 422, wantCode: "invalid_command", wantField: "cancelled_at"},
		{name: "null date", body: `{"idempotency_key":"cancel","subscription_id":"subscription/acme/concurrency_pack","cancelled_at":null}`, wantStatus: 422, wantCode: "invalid_command", wantField: "cancelled_at"},
		{name: "unknown identity", body: `{"idempotency_key":"cancel","subscription_id":"unknown","cancelled_at":"2026-10-31"}`, wantStatus: 404, wantCode: "not_found"},
		{name: "duplicate date", body: `{"idempotency_key":"cancel","subscription_id":"subscription/acme/concurrency_pack","cancelled_at":"2026-10-31","cancelled_at":"2026-11-01"}`, wantStatus: 400, wantCode: "invalid_json"},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			pool := billingDatabase(t)
			store := newPostgresStore(t, pool, postgres.Config{})
			if _, err := store.PurchaseAddon(context.Background(), "acme", "purchase", billing.AddonPurchase{AddonName: "concurrency_pack", PurchasedAt: parseBillingTime(t, "2026-10-01T00:00:00Z")}); err != nil {
				t.Fatal(err)
			}
			before := addonFinancialSnapshot(t, pool)
			result := financialRequest(t, newBillingHandler(t, store, 5*time.Second, 8), http.MethodPost, "/customers/acme/addons/cancel", scenario.body)
			if result.Code != scenario.wantStatus {
				t.Fatalf("cancel body=%s HTTP=%d response=%s; want %d", scenario.body, result.Code, result.Body, scenario.wantStatus)
			}
			if scenario.wantStatus != 200 {
				var failure struct{ Error struct{ Code, Field string } }
				if err := json.Unmarshal(result.Body.Bytes(), &failure); err != nil {
					t.Fatal(err)
				}
				if failure.Error.Code != scenario.wantCode || failure.Error.Field != scenario.wantField {
					t.Fatalf("cancel body=%s error=%+v; want %s/%s", scenario.body, failure.Error, scenario.wantCode, scenario.wantField)
				}
				if after := addonFinancialSnapshot(t, pool); after != before {
					t.Fatal("rejected HTTP cancellation changed financial state")
				}
				return
			}
			var actual struct {
				CustomerID     string `json:"customer_id"`
				SubscriptionID string `json:"subscription_id"`
				EndMonth       string `json:"end_month"`
				CancelledAt    string `json:"cancelled_at"`
			}
			if err := json.Unmarshal(result.Body.Bytes(), &actual); err != nil {
				t.Fatal(err)
			}
			if actual.CustomerID != scenario.wantResult.CustomerID || actual.SubscriptionID != scenario.wantResult.SubscriptionID || actual.EndMonth != scenario.wantResult.EndMonth || actual.CancelledAt != "2026-10-31" {
				t.Fatalf("cancel response=%+v; want %+v date=2026-10-31", actual, scenario.wantResult)
			}
		})
	}
}

// testAddonReactivation invoices the inclusive end month, skips inactive fees,
// and snapshots the changed catalog price on a later purchase without rewriting
// any original purchase, invoice or command replay.
func testAddonReactivation(t *testing.T) {
	cases := []struct {
		name, purchaseDate string
		wantError          error
		wantID             string
		wantInvoiceCents   []int64
		wantSubscriptions  int
	}{
		{name: "same cancellation month cannot double charge", purchaseDate: "2026-10-31T00:00:00Z", wantError: billing.ErrConflict, wantSubscriptions: 1, wantInvoiceCents: []int64{2000, 0, 0}},
		{name: "first following month can reactivate", purchaseDate: "2026-11-01T00:00:00Z", wantID: "subscription/acme/concurrency_pack/2026-11-01", wantSubscriptions: 2, wantInvoiceCents: []int64{2000, 2500, 2500}},
		{name: "last following month day charges full new price", purchaseDate: "2026-11-30T00:00:00Z", wantID: "subscription/acme/concurrency_pack/2026-11-30", wantSubscriptions: 2, wantInvoiceCents: []int64{2000, 2500, 2500}},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return parseBillingTime(t, "2027-01-01T00:00:00Z") }})
			original, err := store.PurchaseAddon(ctx, "acme", "first", billing.AddonPurchase{AddonName: "concurrency_pack", PurchasedAt: parseBillingTime(t, "2026-10-01T00:00:00Z")})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.CancelAddon(ctx, "acme", "cancel", billing.AddonCancellation{SubscriptionID: original.SubscriptionID, CancelledAt: parseBillingTime(t, "2026-10-31T00:00:00Z")}); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "UPDATE addons SET monthly_price_cents=2500 WHERE addon_name='concurrency_pack'"); err != nil {
				t.Fatal(err)
			}
			before := addonFinancialSnapshot(t, pool)
			actual, err := store.PurchaseAddon(ctx, "acme", "second", billing.AddonPurchase{AddonName: "concurrency_pack", PurchasedAt: parseBillingTime(t, scenario.purchaseDate)})
			if !errors.Is(err, scenario.wantError) {
				t.Fatalf("reactivation date=%s error=%v; want %v", scenario.purchaseDate, err, scenario.wantError)
			}
			if err != nil && addonFinancialSnapshot(t, pool) != before {
				t.Fatal("failed reactivation changed financial state")
			}
			if err == nil && actual.SubscriptionID != scenario.wantID {
				t.Fatalf("reactivation ID=%s; want %s", actual.SubscriptionID, scenario.wantID)
			}
			for index, month := range []string{"2026-10", "2026-11", "2026-12"} {
				invoice, err := store.CloseMonth(ctx, "acme", month)
				if err != nil {
					t.Fatal(err)
				}
				if invoice.TotalCents != scenario.wantInvoiceCents[index] {
					t.Fatalf("invoice %s=%+v; want %d cents", month, invoice, scenario.wantInvoiceCents[index])
				}
			}
			replay, err := store.PurchaseAddon(ctx, "acme", "first", billing.AddonPurchase{AddonName: "concurrency_pack", PurchasedAt: parseBillingTime(t, "2026-10-01T00:00:00Z")})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(replay, original) {
				t.Fatalf("original replay=%+v; want %+v", replay, original)
			}
			var count int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM addon_subscriptions").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != scenario.wantSubscriptions {
				t.Fatalf("subscriptions=%d; want %d", count, scenario.wantSubscriptions)
			}
		})
	}
}
