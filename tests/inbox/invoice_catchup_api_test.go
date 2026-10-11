//go:build integration

package inbox_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/postgres"
)

// TestCatchUpInvoicesAPI keeps required transport fields and literal HTTP results
// together; a failed request must preserve all financial and idempotency state.
func TestCatchUpInvoicesAPI(t *testing.T) {
	cases := []struct {
		name, customer, body string
		wantStatus           int
		wantCode, wantField  string
		wantMonths           []string
		wantTotals           []int64
	}{
		{name: "recover missed November and target December", customer: "acme", body: `{"idempotency_key":"recover","through_month":"2026-12"}`, wantStatus: 200, wantMonths: []string{"2026-11", "2026-12"}, wantTotals: []int64{0, 0}},
		{name: "missing operation key", customer: "acme", body: `{"through_month":"2026-12"}`, wantStatus: 422, wantCode: "invalid_command", wantField: "idempotency_key"},
		{name: "missing target", customer: "acme", body: `{"idempotency_key":"recover"}`, wantStatus: 422, wantCode: "invalid_command", wantField: "through_month"},
		{name: "null target", customer: "acme", body: `{"idempotency_key":"recover","through_month":null}`, wantStatus: 422, wantCode: "invalid_command", wantField: "through_month"},
		{name: "invalid month", customer: "acme", body: `{"idempotency_key":"recover","through_month":"2026-13"}`, wantStatus: 422, wantCode: "invalid_command", wantField: "through_month"},
		{name: "current month cannot close missing predecessor", customer: "acme", body: `{"idempotency_key":"recover","through_month":"2027-01"}`, wantStatus: 422, wantCode: "invalid_command", wantField: "through_month"},
		{name: "unknown customer", customer: "missing", body: `{"idempotency_key":"recover","through_month":"2026-12"}`, wantStatus: 404, wantCode: "not_found"},
		{name: "changed property name is not silently accepted", customer: "acme", body: `{"idempotency_key":"recover","month":"2026-12"}`, wantStatus: 400, wantCode: "invalid_json"},
		{name: "duplicate target", customer: "acme", body: `{"idempotency_key":"recover","through_month":"2026-11","through_month":"2026-12"}`, wantStatus: 400, wantCode: "invalid_json"},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return parseBillingTime(t, "2027-01-01T00:00:00Z") }})
			if _, err := store.CloseMonth(ctx, "acme", "2026-10"); err != nil {
				t.Fatal(err)
			}
			before := addonFinancialSnapshot(t, pool)

			response := financialRequest(t, newBillingHandler(t, store, 5*time.Second, 8), http.MethodPost, "/customers/"+scenario.customer+"/invoices/catch-up", scenario.body)
			if response.Code != scenario.wantStatus {
				t.Fatalf("CatchUpInvoices body=%s HTTP=%d body=%s; want %d", scenario.body, response.Code, response.Body, scenario.wantStatus)
			}
			if scenario.wantStatus != 200 {
				var failure struct{ Error struct{ Code, Field string } }
				if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
					t.Fatal(err)
				}
				if failure.Error.Code != scenario.wantCode || failure.Error.Field != scenario.wantField {
					t.Fatalf("catch-up error=%+v; want %s/%s", failure.Error, scenario.wantCode, scenario.wantField)
				}
				if after := addonFinancialSnapshot(t, pool); after != before {
					t.Fatal("failed catch-up HTTP command changed financial state")
				}
				return
			}
			var result billing.InvoiceRun
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.CustomerID != scenario.customer || result.ThroughMonth != "2026-12" || len(result.Invoices) != len(scenario.wantMonths) {
				t.Fatalf("catch-up response=%+v; want customer=%s through=2026-12 months=%v", result, scenario.customer, scenario.wantMonths)
			}
			for index, invoice := range result.Invoices {
				if invoice.Month != scenario.wantMonths[index] || invoice.TotalCents != scenario.wantTotals[index] {
					t.Fatalf("catch-up invoice=%+v; want month=%s total=%d", invoice, scenario.wantMonths[index], scenario.wantTotals[index])
				}
			}
		})
	}
}
