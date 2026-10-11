//go:build integration

package inbox_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/postgres"
)

type financialRejectionStep struct {
	name, body, wantCode, wantField string
	wantStatus                      int
	wantState                       resourceState
	wantLedgerEntries               int
}

// testCreditRejectedState rejects the neighboring zero grant and changed retry
// between valid grants; each error preserves full rows and leaves its key reusable.
func testCreditRejectedState(t *testing.T) {
	testFinancialRejectionSteps(t, "/customers/acme/credits", []financialRejectionStep{
		{name: "zero cannot grant", body: `{"idempotency_key":"grant","amount_cents":0}`, wantStatus: 422, wantCode: "invalid_command", wantField: "amount_cents", wantState: resourceState{prices: 3, creditMicrocents: "0"}},
		{name: "one cent uses rejected key", body: `{"idempotency_key":"grant","amount_cents":1}`, wantStatus: 200, wantState: resourceState{prices: 3, operations: 1, stateVersion: 1, creditMicrocents: "1000000"}, wantLedgerEntries: 1},
		{name: "changed amount conflicts", body: `{"idempotency_key":"grant","amount_cents":2}`, wantStatus: 409, wantCode: "billing_conflict", wantState: resourceState{prices: 3, operations: 1, stateVersion: 1, creditMicrocents: "1000000"}, wantLedgerEntries: 1},
		{name: "original retry still grants once", body: `{"idempotency_key":"grant","amount_cents":1}`, wantStatus: 200, wantState: resourceState{prices: 3, operations: 1, stateVersion: 1, creditMicrocents: "1000000"}, wantLedgerEntries: 1},
		{name: "distinct valid grant still works", body: `{"idempotency_key":"next","amount_cents":2}`, wantStatus: 200, wantState: resourceState{prices: 3, operations: 2, stateVersion: 2, creditMicrocents: "3000000"}, wantLedgerEntries: 2},
	})
}

// testPriceRejectedState pairs the rejected negative price with zero, then
// conflicts with a changed retry without rewriting catalog or account state.
func testPriceRejectedState(t *testing.T) {
	testFinancialRejectionSteps(t, "/prices", []financialRejectionStep{
		{name: "negative price rejected", body: `{"idempotency_key":"price","customer_id":null,"metric":"cpu_seconds","price_per_million_cents":-1,"effective_from":"2026-12-01"}`, wantStatus: 422, wantCode: "invalid_command", wantField: "price_per_million_cents", wantState: resourceState{prices: 3, creditMicrocents: "0"}},
		{name: "zero price uses rejected key", body: `{"idempotency_key":"price","customer_id":null,"metric":"cpu_seconds","price_per_million_cents":0,"effective_from":"2026-12-01"}`, wantStatus: 200, wantState: resourceState{prices: 4, operations: 1, creditMicrocents: "0"}},
		{name: "changed price conflicts", body: `{"idempotency_key":"price","customer_id":null,"metric":"cpu_seconds","price_per_million_cents":1,"effective_from":"2026-12-01"}`, wantStatus: 409, wantCode: "billing_conflict", wantState: resourceState{prices: 4, operations: 1, creditMicrocents: "0"}},
		{name: "original free price still replays", body: `{"idempotency_key":"price","customer_id":null,"metric":"cpu_seconds","price_per_million_cents":0,"effective_from":"2026-12-01"}`, wantStatus: 200, wantState: resourceState{prices: 4, operations: 1, creditMicrocents: "0"}},
		{name: "adjacent effective day still works", body: `{"idempotency_key":"next","customer_id":null,"metric":"cpu_seconds","price_per_million_cents":1,"effective_from":"2026-12-02"}`, wantStatus: 200, wantState: resourceState{prices: 5, operations: 2, creditMicrocents: "0"}},
	})
}

// testAddonRejectedState rejects an invalid calendar day and changed purchase
// content, then recovers with a valid day and the original durable subscription.
func testAddonRejectedState(t *testing.T) {
	testFinancialRejectionSteps(t, "/customers/acme/addons", []financialRejectionStep{
		{name: "nonexistent October day", body: `{"idempotency_key":"purchase","addon_name":"concurrency_pack","purchased_at":"2026-10-32"}`, wantStatus: 422, wantCode: "invalid_command", wantField: "purchased_at", wantState: resourceState{prices: 3, creditMicrocents: "0"}},
		{name: "last October day uses rejected key", body: `{"idempotency_key":"purchase","addon_name":"concurrency_pack","purchased_at":"2026-10-31"}`, wantStatus: 200, wantState: resourceState{prices: 3, subscriptions: 1, operations: 1, stateVersion: 1, creditMicrocents: "0"}},
		{name: "changed purchase conflicts", body: `{"idempotency_key":"purchase","addon_name":"concurrency_pack","purchased_at":"2026-10-30"}`, wantStatus: 409, wantCode: "billing_conflict", wantState: resourceState{prices: 3, subscriptions: 1, operations: 1, stateVersion: 1, creditMicrocents: "0"}},
		{name: "original subscription still replays", body: `{"idempotency_key":"purchase","addon_name":"concurrency_pack","purchased_at":"2026-10-31"}`, wantStatus: 200, wantState: resourceState{prices: 3, subscriptions: 1, operations: 1, stateVersion: 1, creditMicrocents: "0"}},
	})
}

// testFinancialRejectionSteps executes the caller's declared workflow in its own
// schema. Full-row comparisons expose side effects; literal states check success.
func testFinancialRejectionSteps(t *testing.T, path string, steps []financialRejectionStep) {
	pool := billingDatabase(t)
	handler := resourceHandler(t, pool)
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			before := addonFinancialSnapshot(t, pool)
			response := financialRequest(t, handler, http.MethodPost, path, step.body)

			if response.Code != step.wantStatus {
				t.Fatalf("POST %s(%s): HTTP=%d body=%s; want %d", path, step.body, response.Code, response.Body, step.wantStatus)
			}
			var result struct {
				Error struct{ Code, Field string } `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Error.Code != step.wantCode || result.Error.Field != step.wantField {
				t.Fatalf("POST %s(%s): error=%+v; want code=%s field=%s", path, step.body, result.Error, step.wantCode, step.wantField)
			}

			if step.wantStatus != 200 {
				if after := addonFinancialSnapshot(t, pool); after != before {
					t.Fatalf("Rejected POST %s(%s) changed state: before=%s after=%s", path, step.body, before, after)
				}
			}
			assertResourceState(t, pool, step.wantState)
			var entries int
			if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM credit_entries").Scan(&entries); err != nil {
				t.Fatal(err)
			}
			if entries != step.wantLedgerEntries {
				t.Fatalf("POST %s(%s): ledger entries=%d; want %d", path, step.body, entries, step.wantLedgerEntries)
			}
		})
	}
}

// testClosingRejectedState keeps nonempty credit, ledger, subscription, rated
// usage and monthly gross intact after a skipped close, then verifies every
// financial field of the adjacent valid invoice and its immutable replay.
func testClosingRejectedState(t *testing.T) {
	scenario := struct {
		customer, receiptTime string
		creditCents           int64
		usage                 []measuredHour
		purchase              billing.AddonPurchase
		wantRejectedStatus    int
		wantRejectedCode      string
		wantInvoice           billing.Invoice
	}{customer: "acme", receiptTime: "2026-10-31T23:00:00Z", creditCents: 1,
		usage:              []measuredHour{{eventID: "rated", start: "2026-10-10T12:00:00Z", units: 100_000}},
		purchase:           billing.AddonPurchase{AddonName: "concurrency_pack", PurchasedAt: time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)},
		wantRejectedStatus: 409, wantRejectedCode: "billing_conflict",
		wantInvoice: billing.Invoice{CustomerID: "acme", Month: "2026-10", Number: "ACME-0001", CustomerName: "Acme Inc.", Country: "US", BillingAddress: "1 Market St, San Francisco, CA 94105", Currency: "USD", TotalCents: 2000, GrossUsageMicrocents: "400000", CreditUsedMicrocents: "400000", IssuedAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), Lines: []billing.InvoiceLine{
			{Kind: "usage", Description: "Usage", AmountCents: 0, PriceVersionID: "cpu-acme-2026-10-01", Metric: "cpu_seconds", UsageMonth: "2026-10", Units: "100000", GrossMicrocents: "400000", CreditMicrocents: "400000"},
			{Kind: "addon", Description: "concurrency_pack", SubscriptionID: "subscription/acme/concurrency_pack", AmountCents: 2000},
			{Kind: "credit", Description: "Used credit", AmountCents: 0},
		}},
	}
	pool := billingDatabase(t)
	ctx := context.Background()
	store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return scenario.wantInvoice.IssuedAt }})
	if err := store.GrantCredit(ctx, scenario.customer, billing.CreditGrant{IdempotencyKey: "grant", AmountCents: scenario.creditCents}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PurchaseAddon(ctx, scenario.customer, "purchase", scenario.purchase); err != nil {
		t.Fatal(err)
	}
	insertMeasuredHours(t, store, scenario.customer, scenario.usage, scenario.receiptTime)
	processUsageSteps(t, store, []bool{true, false})
	handler := newBillingHandler(t, store, 5*time.Second, 8)
	before := addonFinancialSnapshot(t, pool)

	rejected := financialRequest(t, handler, http.MethodPost, "/customers/acme/invoices", `{"month":"2026-11"}`)

	if rejected.Code != scenario.wantRejectedStatus {
		t.Fatalf("Skipped closing: HTTP=%d body=%s; want %d", rejected.Code, rejected.Body, scenario.wantRejectedStatus)
	}
	var failure struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(rejected.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != scenario.wantRejectedCode {
		t.Fatalf("Skipped closing error=%s; want %s", failure.Error.Code, scenario.wantRejectedCode)
	}
	if after := addonFinancialSnapshot(t, pool); after != before {
		t.Fatalf("Rejected closing changed state: before=%s after=%s", before, after)
	}

	invoice, err := store.CloseMonth(ctx, scenario.customer, scenario.wantInvoice.Month)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(invoice, scenario.wantInvoice) {
		t.Fatalf("Recovered invoice=%+v; want %+v", invoice, scenario.wantInvoice)
	}
	beforeReplay := addonFinancialSnapshot(t, pool)
	replay, err := store.CloseMonth(ctx, scenario.customer, scenario.wantInvoice.Month)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replay, scenario.wantInvoice) {
		t.Fatalf("Replayed invoice=%+v; want %+v", replay, scenario.wantInvoice)
	}
	if after := addonFinancialSnapshot(t, pool); after != beforeReplay {
		t.Fatalf("Invoice replay changed state: before=%s after=%s", beforeReplay, after)
	}
}
