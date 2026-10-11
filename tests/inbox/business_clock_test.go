//go:build integration

package inbox_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/postgres"
)

// testSharedBusinessClock exercises transport, accounting and invoice publication
// with one provider in a private schema. Retries retain original receipt and
// ledger times; the adjacent closing boundary preserves all financial effects.
func testSharedBusinessClock(t *testing.T) {
	cases := []struct {
		name, receiptTime, processTime, closeTime                          string
		grantJSON, addonJSON, usageJSON, invoiceJSON                       string
		wantGrantStatus, wantAddonStatus, wantUsageStatus, wantCloseStatus int
		wantErrorField, wantErrorCode, wantInvoiceTime, wantNumber         string
		wantTotalCents                                                     int64
		wantInvoices, wantRatings, wantLedgerEntries                       int
		wantCreditTicks                                                    string
		wantStateVersion                                                   int64
	}{
		{
			name:        "last microsecond rejects closure without publication",
			receiptTime: "2026-10-31T23:59:58Z", processTime: "2026-10-31T23:59:59Z", closeTime: "2026-10-31T23:59:59.999999Z",
			grantJSON:       `{"amount_cents":100,"idempotency_key":"clock-credit"}`,
			addonJSON:       `{"addon_name":"concurrency_pack","purchased_at":"2026-10-05","idempotency_key":"clock-addon"}`,
			usageJSON:       `{"events":[{"source":"clock-scenario","event_id":"one","schema_version":1,"customer_id":"acme","sandbox_id":"sandbox-a","metric":"cpu_seconds","period_start":"2026-10-10T12:00:00Z","period_end":"2026-10-10T13:00:00Z","units":100000000}]}`,
			invoiceJSON:     `{"month":"2026-10"}`,
			wantGrantStatus: 200, wantAddonStatus: 200, wantUsageStatus: 202, wantCloseStatus: 422,
			wantErrorField: "month", wantErrorCode: "invalid_command", wantInvoices: 0, wantRatings: 1, wantLedgerEntries: 2, wantCreditTicks: "0", wantStateVersion: 3,
		},
		{
			name:        "exact UTC boundary publishes the same invoice on retry",
			receiptTime: "2026-10-31T23:59:58Z", processTime: "2026-10-31T23:59:59Z", closeTime: "2026-11-01T00:00:00Z",
			grantJSON:       `{"amount_cents":100,"idempotency_key":"clock-credit"}`,
			addonJSON:       `{"addon_name":"concurrency_pack","purchased_at":"2026-10-05","idempotency_key":"clock-addon"}`,
			usageJSON:       `{"events":[{"source":"clock-scenario","event_id":"one","schema_version":1,"customer_id":"acme","sandbox_id":"sandbox-a","metric":"cpu_seconds","period_start":"2026-10-10T12:00:00Z","period_end":"2026-10-10T13:00:00Z","units":100000000}]}`,
			invoiceJSON:     `{"month":"2026-10"}`,
			wantGrantStatus: 200, wantAddonStatus: 200, wantUsageStatus: 202, wantCloseStatus: 200,
			wantInvoiceTime: "2026-11-01T00:00:00Z", wantNumber: "ACME-0001", wantTotalCents: 2300, wantInvoices: 1, wantRatings: 1, wantLedgerEntries: 2, wantCreditTicks: "0", wantStateVersion: 4,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			instant := parseBillingTime(t, tc.receiptTime)
			now := func() time.Time { return instant }
			store := newPostgresStore(t, pool, postgres.Config{Now: now})
			handler := newBillingHandlerWithClock(t, store, time.Second, 8, now)
			steps := []struct {
				path, body string
				wantStatus int
			}{
				{path: "/customers/acme/credits", body: tc.grantJSON, wantStatus: tc.wantGrantStatus},
				{path: "/customers/acme/addons", body: tc.addonJSON, wantStatus: tc.wantAddonStatus},
				{path: "/usage/batches", body: tc.usageJSON, wantStatus: tc.wantUsageStatus},
			}
			for _, step := range steps {
				response := clockRequest(t, handler, "POST", step.path, step.body)
				if response.Code != step.wantStatus {
					t.Fatalf("POST %s body=%s: status=%d body=%s; want %d", step.path, step.body, response.Code, response.Body, step.wantStatus)
				}
			}

			instant = parseBillingTime(t, tc.processTime)
			worked, err := store.ProcessBatch(ctx)
			if err != nil {
				t.Fatalf("ProcessBatch at %s: %v", tc.processTime, err)
			}
			if !worked {
				t.Fatalf("ProcessBatch at %s worked=false; want true", tc.processTime)
			}

			instant = parseBillingTime(t, tc.closeTime)
			first := clockRequest(t, handler, "POST", "/customers/acme/invoices", tc.invoiceJSON)
			if first.Code != tc.wantCloseStatus {
				t.Fatalf("Close at %s: status=%d body=%s; want %d", tc.closeTime, first.Code, first.Body, tc.wantCloseStatus)
			}
			if tc.wantCloseStatus == 200 {
				var invoice billing.Invoice
				if err := json.Unmarshal(first.Body.Bytes(), &invoice); err != nil {
					t.Fatalf("Decode invoice: %v", err)
				}
				if invoice.Number != tc.wantNumber || invoice.TotalCents != tc.wantTotalCents || invoice.IssuedAt.Format(time.RFC3339Nano) != tc.wantInvoiceTime {
					t.Errorf("Close at %s invoice=%+v; want number=%s total=%d issued=%s", tc.closeTime, invoice, tc.wantNumber, tc.wantTotalCents, tc.wantInvoiceTime)
				}
			} else {
				var response struct{ Error struct{ Code, Field string } }
				if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
					t.Fatalf("Decode closing error: %v", err)
				}
				if response.Error.Code != tc.wantErrorCode || response.Error.Field != tc.wantErrorField {
					t.Errorf("Close at %s error=%+v; want code=%s field=%s", tc.closeTime, response.Error, tc.wantErrorCode, tc.wantErrorField)
				}
			}

			// Advancing the provider must not rewrite original receipt or grant audit time.
			instant = instant.Add(time.Second)
			for _, step := range steps {
				response := clockRequest(t, handler, "POST", step.path, step.body)
				if response.Code != step.wantStatus {
					t.Fatalf("Retry %s status=%d; want %d", step.path, response.Code, step.wantStatus)
				}
			}
			if tc.wantCloseStatus == 200 {
				retry := clockRequest(t, handler, "POST", "/customers/acme/invoices", tc.invoiceJSON)
				if retry.Code != 200 || retry.Body.String() != first.Body.String() {
					t.Errorf("Retry invoice=%d %s; want original %s", retry.Code, retry.Body, first.Body)
				}
			}

			var receivedAt, processedAt, grantAt, debitAt time.Time
			err = pool.QueryRow(ctx, `SELECT received_at,processed_at,
                (SELECT recorded_at FROM credit_entries WHERE amount_ticks>0),
                (SELECT recorded_at FROM credit_entries WHERE amount_ticks<0) FROM usage_inbox`).Scan(&receivedAt, &processedAt, &grantAt, &debitAt)
			if err != nil {
				t.Fatalf("Read clock audit state: %v", err)
			}
			if receivedAt.Format(time.RFC3339Nano) != tc.receiptTime || grantAt.Format(time.RFC3339Nano) != tc.receiptTime || processedAt.Format(time.RFC3339Nano) != tc.processTime || debitAt.Format(time.RFC3339Nano) != tc.processTime {
				t.Errorf("Clock audit received=%s grant=%s processed=%s debit=%s; want receipt/grant=%s processing/debit=%s", receivedAt, grantAt, processedAt, debitAt, tc.receiptTime, tc.processTime)
			}

			var invoices, ratings, ledgerEntries int
			var credit string
			var version int64
			err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM invoices),(SELECT count(*) FROM usage_ratings),
                (SELECT count(*) FROM credit_entries),credit_balance_ticks::text,state_version FROM customer_billing_state WHERE customer_id='acme'`).Scan(&invoices, &ratings, &ledgerEntries, &credit, &version)
			if err != nil {
				t.Fatalf("Read financial state after clock scenario: %v", err)
			}
			if invoices != tc.wantInvoices || ratings != tc.wantRatings || ledgerEntries != tc.wantLedgerEntries || credit != tc.wantCreditTicks || version != tc.wantStateVersion {
				t.Errorf("Clock scenario invoices=%d ratings=%d ledger=%d credit=%s version=%d; want %d %d %d %s %d", invoices, ratings, ledgerEntries, credit, version, tc.wantInvoices, tc.wantRatings, tc.wantLedgerEntries, tc.wantCreditTicks, tc.wantStateVersion)
			}
		})
	}
}

// clockRequest supplies HTTP plumbing while inputs and statuses remain visible
// in each shared-clock scenario; the handler owns receipt and financial time.
func clockRequest(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	return response
}
