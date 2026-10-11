//go:build integration

package inbox_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

type addonClosingStep struct {
	name           string
	month          string
	wantStatus     int
	wantErrorCode  string
	wantNumber     string
	wantTotalCents int64
	wantLines      []billing.InvoiceLine
	wantState      addonClosingState
}

type addonClosingState struct {
	invoices, closedMonths, subscriptions, operations, creditEntries int
	nextNumber, version, invoiceCents                                int64
	creditMicrocents                                                 string
	usageRows                                                        int
}

// testClosingAddonMonths sends purchases and invoice requests through the public
// router in isolated schemas with no usage. Skipped active months must conflict
// without any writes; chronological recovery bills every month exactly once.
// Purchase-month boundaries, future subscriptions, free add-ons, customer scope,
// credit preservation and both database time zones retain their adjacent behavior.
func testClosingAddonMonths(t *testing.T) {
	cases := []struct {
		name, timeZone, serverTime, purchaseDate, purchaseCustomer string
		monthlyPriceCents, creditCents                             int64
		steps                                                      []addonClosingStep
	}{
		{
			name: "older add-on alone blocks November", timeZone: "UTC", serverTime: "2026-12-01T00:00:00Z", purchaseDate: "2026-10-05", purchaseCustomer: "acme", monthlyPriceCents: 2000,
			steps: []addonClosingStep{
				{name: "reject skipped October", month: "2026-11", wantStatus: 409, wantErrorCode: "billing_conflict", wantState: addonClosingState{subscriptions: 1, operations: 1, nextNumber: 1, version: 1, creditMicrocents: "0"}},
				{name: "repeated rejection remains harmless", month: "2026-11", wantStatus: 409, wantErrorCode: "billing_conflict", wantState: addonClosingState{subscriptions: 1, operations: 1, nextNumber: 1, version: 1, creditMicrocents: "0"}},
				{name: "close October with the first number", month: "2026-10", wantStatus: 200, wantNumber: "ACME-0001", wantTotalCents: 2000, wantLines: []billing.InvoiceLine{{Kind: "addon", Description: "concurrency_pack", SubscriptionID: "subscription/acme/concurrency_pack", AmountCents: 2000}, {Kind: "credit", Description: "Used credit", AmountCents: 0}}, wantState: addonClosingState{invoices: 1, closedMonths: 1, subscriptions: 1, operations: 1, nextNumber: 2, version: 2, invoiceCents: 2000, creditMicrocents: "0"}},
				{name: "close November with the second fee", month: "2026-11", wantStatus: 200, wantNumber: "ACME-0002", wantTotalCents: 2000, wantLines: []billing.InvoiceLine{{Kind: "addon", Description: "concurrency_pack", SubscriptionID: "subscription/acme/concurrency_pack", AmountCents: 2000}, {Kind: "credit", Description: "Used credit", AmountCents: 0}}, wantState: addonClosingState{invoices: 2, closedMonths: 2, subscriptions: 1, operations: 1, nextNumber: 3, version: 3, invoiceCents: 4000, creditMicrocents: "0"}},
			},
		},
		{
			name: "multiple skipped months preserve credit in Shanghai", timeZone: "Asia/Shanghai", serverTime: "2027-01-01T00:00:00Z", purchaseDate: "2026-10-31", purchaseCustomer: "acme", monthlyPriceCents: 2000, creditCents: 2500,
			steps: []addonClosingStep{
				{name: "reject skipped October and November", month: "2026-12", wantStatus: 409, wantErrorCode: "billing_conflict", wantState: addonClosingState{subscriptions: 1, operations: 2, creditEntries: 1, nextNumber: 1, version: 2, creditMicrocents: "2500000000"}},
				{name: "last purchase day still owes full October", month: "2026-10", wantStatus: 200, wantNumber: "ACME-0001", wantTotalCents: 2000, wantLines: []billing.InvoiceLine{{Kind: "addon", Description: "concurrency_pack", SubscriptionID: "subscription/acme/concurrency_pack", AmountCents: 2000}, {Kind: "credit", Description: "Used credit", AmountCents: 0}}, wantState: addonClosingState{invoices: 1, closedMonths: 1, subscriptions: 1, operations: 2, creditEntries: 1, nextNumber: 2, version: 3, invoiceCents: 2000, creditMicrocents: "2500000000"}},
				{name: "later closing also cannot skip November", month: "2026-12", wantStatus: 409, wantErrorCode: "billing_conflict", wantState: addonClosingState{invoices: 1, closedMonths: 1, subscriptions: 1, operations: 2, creditEntries: 1, nextNumber: 2, version: 3, invoiceCents: 2000, creditMicrocents: "2500000000"}},
				{name: "recover November", month: "2026-11", wantStatus: 200, wantNumber: "ACME-0002", wantTotalCents: 2000, wantLines: []billing.InvoiceLine{{Kind: "addon", Description: "concurrency_pack", SubscriptionID: "subscription/acme/concurrency_pack", AmountCents: 2000}, {Kind: "credit", Description: "Used credit", AmountCents: 0}}, wantState: addonClosingState{invoices: 2, closedMonths: 2, subscriptions: 1, operations: 2, creditEntries: 1, nextNumber: 3, version: 4, invoiceCents: 4000, creditMicrocents: "2500000000"}},
				{name: "recover December without consuming credit", month: "2026-12", wantStatus: 200, wantNumber: "ACME-0003", wantTotalCents: 2000, wantLines: []billing.InvoiceLine{{Kind: "addon", Description: "concurrency_pack", SubscriptionID: "subscription/acme/concurrency_pack", AmountCents: 2000}, {Kind: "credit", Description: "Used credit", AmountCents: 0}}, wantState: addonClosingState{invoices: 3, closedMonths: 3, subscriptions: 1, operations: 2, creditEntries: 1, nextNumber: 4, version: 5, invoiceCents: 6000, creditMicrocents: "2500000000"}},
			},
		},
		{
			name: "first day of target month is valid", timeZone: "Asia/Shanghai", serverTime: "2026-12-01T00:00:00Z", purchaseDate: "2026-11-01", purchaseCustomer: "acme", monthlyPriceCents: 2000,
			steps: []addonClosingStep{
				{name: "November is the first active month", month: "2026-11", wantStatus: 200, wantNumber: "ACME-0001", wantTotalCents: 2000, wantLines: []billing.InvoiceLine{{Kind: "addon", Description: "concurrency_pack", SubscriptionID: "subscription/acme/concurrency_pack", AmountCents: 2000}, {Kind: "credit", Description: "Used credit", AmountCents: 0}}, wantState: addonClosingState{invoices: 1, closedMonths: 1, subscriptions: 1, operations: 1, nextNumber: 2, version: 2, invoiceCents: 2000, creditMicrocents: "0"}},
			},
		},
		{
			name: "future subscription does not block earlier invoice", timeZone: "UTC", serverTime: "2027-01-01T00:00:00Z", purchaseDate: "2026-12-01", purchaseCustomer: "acme", monthlyPriceCents: 2000,
			steps: []addonClosingStep{
				{name: "November has no fee before activation", month: "2026-11", wantStatus: 200, wantNumber: "ACME-0001", wantTotalCents: 0, wantLines: []billing.InvoiceLine{{Kind: "credit", Description: "Used credit", AmountCents: 0}}, wantState: addonClosingState{invoices: 1, closedMonths: 1, subscriptions: 1, operations: 1, nextNumber: 2, version: 2, creditMicrocents: "0"}},
				{name: "December starts charging", month: "2026-12", wantStatus: 200, wantNumber: "ACME-0002", wantTotalCents: 2000, wantLines: []billing.InvoiceLine{{Kind: "addon", Description: "concurrency_pack", SubscriptionID: "subscription/acme/concurrency_pack", AmountCents: 2000}, {Kind: "credit", Description: "Used credit", AmountCents: 0}}, wantState: addonClosingState{invoices: 2, closedMonths: 2, subscriptions: 1, operations: 1, nextNumber: 3, version: 3, invoiceCents: 2000, creditMicrocents: "0"}},
			},
		},
		{
			name: "another customer's older subscription is isolated", timeZone: "UTC", serverTime: "2026-12-01T00:00:00Z", purchaseDate: "2026-10-05", purchaseCustomer: "cyberdyne", monthlyPriceCents: 2000,
			steps: []addonClosingStep{
				{name: "Acme may close its empty November", month: "2026-11", wantStatus: 200, wantNumber: "ACME-0001", wantTotalCents: 0, wantLines: []billing.InvoiceLine{{Kind: "credit", Description: "Used credit", AmountCents: 0}}, wantState: addonClosingState{invoices: 1, closedMonths: 1, subscriptions: 1, operations: 1, nextNumber: 2, version: 1, creditMicrocents: "0"}},
			},
		},
		{
			name: "free active add-on also preserves monthly history", timeZone: "UTC", serverTime: "2026-12-01T00:00:00Z", purchaseDate: "2026-10-05", purchaseCustomer: "acme", monthlyPriceCents: 0,
			steps: []addonClosingStep{
				{name: "October is still required", month: "2026-11", wantStatus: 409, wantErrorCode: "billing_conflict", wantState: addonClosingState{subscriptions: 1, operations: 1, nextNumber: 1, version: 1, creditMicrocents: "0"}},
				{name: "October contains the free add-on line", month: "2026-10", wantStatus: 200, wantNumber: "ACME-0001", wantTotalCents: 0, wantLines: []billing.InvoiceLine{{Kind: "addon", Description: "concurrency_pack", SubscriptionID: "subscription/acme/concurrency_pack", AmountCents: 0}, {Kind: "credit", Description: "Used credit", AmountCents: 0}}, wantState: addonClosingState{invoices: 1, closedMonths: 1, subscriptions: 1, operations: 1, nextNumber: 2, version: 2, creditMicrocents: "0"}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			originalPool := billingDatabase(t)
			ctx := context.Background()
			config := originalPool.Config()
			config.ConnConfig.RuntimeParams["timezone"] = tc.timeZone
			pool, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			if _, err := pool.Exec(ctx, "UPDATE addons SET monthly_price_cents=$1 WHERE addon_name='concurrency_pack'", tc.monthlyPriceCents); err != nil {
				t.Fatal(err)
			}
			clock := parseBillingTime(t, tc.serverTime)
			handler := newBillingHandler(t, newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return clock }}), 5*time.Second, 8)
			if tc.creditCents > 0 {
				body, err := json.Marshal(billing.CreditGrant{IdempotencyKey: "addon-credit", AmountCents: tc.creditCents})
				if err != nil {
					t.Fatal(err)
				}
				response := financialRequest(t, handler, http.MethodPost, "/customers/acme/credits", string(body))
				if response.Code != 200 {
					t.Fatalf("Grant credit=%d: HTTP=%d body=%s; want 200", tc.creditCents, response.Code, response.Body)
				}
			}
			purchaseBody := `{"idempotency_key":"addon-purchase","addon_name":"concurrency_pack","purchased_at":"` + tc.purchaseDate + `"}`
			purchase := financialRequest(t, handler, http.MethodPost, "/customers/"+tc.purchaseCustomer+"/addons", purchaseBody)
			if purchase.Code != 200 {
				t.Fatalf("Purchase(%s customer=%s): HTTP=%d body=%s; want 200", purchaseBody, tc.purchaseCustomer, purchase.Code, purchase.Body)
			}

			for _, step := range tc.steps {
				t.Run(step.name, func(t *testing.T) {
					before := addonFinancialSnapshot(t, pool)
					response := financialRequest(t, handler, http.MethodPost, "/customers/acme/invoices", `{"month":"`+step.month+`"}`)

					assertAddonClosingResponse(t, response, step)
					assertAddonClosingState(t, pool, step.wantState)
					if step.wantStatus != 200 {
						if after := addonFinancialSnapshot(t, pool); after != before {
							t.Fatalf("Rejected CloseMonth(%s) changed financial data: before=%s after=%s", step.month, before, after)
						}
						missing := financialRequest(t, handler, http.MethodGet, "/customers/acme/invoices/"+step.month, "")
						if missing.Code != 404 {
							t.Fatalf("GET rejected invoice(%s): HTTP=%d body=%s; want 404", step.month, missing.Code, missing.Body)
						}

						return
					}

					beforeReplay := addonFinancialSnapshot(t, pool)
					for _, method := range []string{http.MethodPost, http.MethodGet} {
						path, body := "/customers/acme/invoices", `{"month":"`+step.month+`"}`
						if method == http.MethodGet {
							path, body = path+"/"+step.month, ""
						}
						replay := financialRequest(t, handler, method, path, body)
						if replay.Code != 200 || replay.Body.String() != response.Body.String() {
							t.Fatalf("%s invoice(%s): HTTP=%d body=%s; want 200 and original=%s", method, step.month, replay.Code, replay.Body, response.Body)
						}
					}
					if after := addonFinancialSnapshot(t, pool); after != beforeReplay {
						t.Fatalf("Invoice replay(%s) changed financial data: before=%s after=%s", step.month, beforeReplay, after)
					}
				})
			}
		})
	}
}

// assertAddonClosingResponse checks HTTP errors before invoice fields and compares
// the full literal line list, including the zero credit line and exact audit totals.
func assertAddonClosingResponse(t *testing.T, response *httptest.ResponseRecorder, step addonClosingStep) {
	t.Helper()
	if response.Code != step.wantStatus {
		t.Fatalf("CloseMonth(%s): HTTP=%d body=%s; want %d", step.month, response.Code, response.Body, step.wantStatus)
	}
	if step.wantErrorCode != "" {
		var result struct {
			Error struct{ Code string } `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Error.Code != step.wantErrorCode {
			t.Fatalf("CloseMonth(%s): error=%q; want %q", step.month, result.Error.Code, step.wantErrorCode)
		}

		return
	}

	var invoice billing.Invoice
	if err := json.Unmarshal(response.Body.Bytes(), &invoice); err != nil {
		t.Fatal(err)
	}
	if invoice.Number != step.wantNumber || invoice.Month != step.month || invoice.TotalCents != step.wantTotalCents || !reflect.DeepEqual(invoice.Lines, step.wantLines) {
		t.Fatalf("CloseMonth(%s): invoice=%+v; want number=%s total=%d lines=%+v", step.month, invoice, step.wantNumber, step.wantTotalCents, step.wantLines)
	}
	if invoice.GrossUsageMicrocents != "0" || invoice.CreditUsedMicrocents != "0" {
		t.Fatalf("Add-on-only invoice(%s): gross=%s credit=%s; want 0 and 0", step.month, invoice.GrossUsageMicrocents, invoice.CreditUsedMicrocents)
	}
}

// assertAddonClosingState verifies numbering, totals, closures, subscriptions,
// ledger and credit against declared state; these no-usage scenarios may not rate
// events, create groups or frozen links, or change original-month gross spend.
func assertAddonClosingState(t *testing.T, pool *pgxpool.Pool, want addonClosingState) {
	t.Helper()
	var got addonClosingState
	err := pool.QueryRow(context.Background(), `SELECT
        (SELECT count(*) FROM invoices), (SELECT count(*) FROM closed_billing_months),
        (SELECT count(*) FROM addon_subscriptions), (SELECT count(*) FROM api_idempotency_operations),
        (SELECT count(*) FROM credit_entries), next_invoice_number, state_version,
        (SELECT COALESCE(sum(total_cents),0) FROM invoices), credit_balance_microcents::text,
        (SELECT count(*) FROM usage_inbox)+(SELECT count(*) FROM usage_ratings)+
        (SELECT count(*) FROM rated_usage_groups)+(SELECT count(*) FROM monthly_usage)+
        (SELECT count(*) FROM invoiced_usage_groups)
        FROM customer_billing_state WHERE customer_id='acme'`).Scan(
		&got.invoices, &got.closedMonths, &got.subscriptions, &got.operations, &got.creditEntries,
		&got.nextNumber, &got.version, &got.invoiceCents, &got.creditMicrocents, &got.usageRows)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Add-on closing financial state=%+v; want %+v", got, want)
	}
}

// addonFinancialSnapshot captures complete committed invoice, subscription,
// command, ledger, catalog, account and usage rows in stable order. Rejections
// and retries must preserve every projection, including frozen groups and receipts.
func addonFinancialSnapshot(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var snapshot string
	err := pool.QueryRow(context.Background(), `SELECT jsonb_build_object(
        'invoices',(SELECT jsonb_agg(i ORDER BY customer_id,billing_month) FROM invoices i),
        'closed',(SELECT jsonb_agg(m ORDER BY customer_id,billing_month) FROM closed_billing_months m),
        'subscriptions',(SELECT jsonb_agg(s ORDER BY subscription_id) FROM addon_subscriptions s),
        'cancellations',(SELECT jsonb_agg(c ORDER BY subscription_id) FROM addon_cancellations c),
        'operations',(SELECT jsonb_agg(o ORDER BY operation_scope,idempotency_key) FROM api_idempotency_operations o),
        'credit',(SELECT jsonb_agg(e ORDER BY credit_entry_id) FROM credit_entries e),
        'accounts',(SELECT jsonb_agg(a ORDER BY customer_id) FROM customer_billing_state a),
        'prices',(SELECT jsonb_agg(p ORDER BY price_version_id) FROM price_versions p),
        'receipts',(SELECT jsonb_agg(r ORDER BY source,event_id) FROM usage_inbox r),
        'ratings',(SELECT jsonb_agg(r ORDER BY source,event_id) FROM usage_ratings r),
        'groups',(SELECT jsonb_agg(g ORDER BY group_id) FROM rated_usage_groups g),
        'monthly',(SELECT jsonb_agg(m ORDER BY customer_id,usage_month) FROM monthly_usage m),
        'frozen',(SELECT jsonb_agg(f ORDER BY group_id) FROM invoiced_usage_groups f))::text`).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}

	return snapshot
}

// testClosingAddonOrder controls both winners through database locks rather than
// timing. A committed older purchase blocks a later first close; a close that wins
// rejects a backdated purchase. Same-month purchases are billed or rejected
// atomically. Both outcomes use public HTTP commands in private schemas.
func testClosingAddonOrder(t *testing.T) {
	cases := []struct {
		name, purchaseDate, closingMonth, blockingSQL string
		purchaseFirst                                 bool
		wantPurchaseStatus                            int
		wantClose                                     addonClosingStep
	}{
		{
			name: "older purchase wins before November closing", purchaseFirst: true, purchaseDate: "2026-10-05", closingMonth: "2026-11",
			blockingSQL: `CREATE FUNCTION pause_addon_write() RETURNS trigger LANGUAGE plpgsql AS $f$ BEGIN PERFORM pg_advisory_xact_lock(987002); RETURN NEW; END; $f$;
                CREATE TRIGGER pause_write BEFORE INSERT ON addon_subscriptions FOR EACH ROW EXECUTE FUNCTION pause_addon_write()`,
			wantPurchaseStatus: 200, wantClose: addonClosingStep{month: "2026-11", wantStatus: 409, wantErrorCode: "billing_conflict", wantState: addonClosingState{subscriptions: 1, operations: 1, nextNumber: 1, version: 1, creditMicrocents: "0"}},
		},
		{
			name: "November closing wins before older purchase", purchaseFirst: false, purchaseDate: "2026-10-05", closingMonth: "2026-11",
			blockingSQL: `CREATE FUNCTION pause_addon_write() RETURNS trigger LANGUAGE plpgsql AS $f$ BEGIN PERFORM pg_advisory_xact_lock(987002); RETURN NEW; END; $f$;
                CREATE TRIGGER pause_write BEFORE INSERT ON invoices FOR EACH ROW EXECUTE FUNCTION pause_addon_write()`,
			wantPurchaseStatus: 409, wantClose: addonClosingStep{month: "2026-11", wantStatus: 200, wantNumber: "ACME-0001", wantTotalCents: 0, wantLines: []billing.InvoiceLine{{Kind: "credit", Description: "Used credit", AmountCents: 0}}, wantState: addonClosingState{invoices: 1, closedMonths: 1, nextNumber: 2, version: 1, creditMicrocents: "0"}},
		},
		{
			name: "same-month purchase wins and is charged", purchaseFirst: true, purchaseDate: "2026-11-30", closingMonth: "2026-11",
			blockingSQL: `CREATE FUNCTION pause_addon_write() RETURNS trigger LANGUAGE plpgsql AS $f$ BEGIN PERFORM pg_advisory_xact_lock(987002); RETURN NEW; END; $f$;
                CREATE TRIGGER pause_write BEFORE INSERT ON addon_subscriptions FOR EACH ROW EXECUTE FUNCTION pause_addon_write()`,
			wantPurchaseStatus: 200, wantClose: addonClosingStep{month: "2026-11", wantStatus: 200, wantNumber: "ACME-0001", wantTotalCents: 2000, wantLines: []billing.InvoiceLine{{Kind: "addon", Description: "concurrency_pack", SubscriptionID: "subscription/acme/concurrency_pack", AmountCents: 2000}, {Kind: "credit", Description: "Used credit", AmountCents: 0}}, wantState: addonClosingState{invoices: 1, closedMonths: 1, subscriptions: 1, operations: 1, nextNumber: 2, version: 2, invoiceCents: 2000, creditMicrocents: "0"}},
		},
		{
			name: "same-month closing wins and rejects purchase", purchaseFirst: false, purchaseDate: "2026-11-30", closingMonth: "2026-11",
			blockingSQL: `CREATE FUNCTION pause_addon_write() RETURNS trigger LANGUAGE plpgsql AS $f$ BEGIN PERFORM pg_advisory_xact_lock(987002); RETURN NEW; END; $f$;
                CREATE TRIGGER pause_write BEFORE INSERT ON invoices FOR EACH ROW EXECUTE FUNCTION pause_addon_write()`,
			wantPurchaseStatus: 409, wantClose: addonClosingStep{month: "2026-11", wantStatus: 200, wantNumber: "ACME-0001", wantTotalCents: 0, wantLines: []billing.InvoiceLine{{Kind: "credit", Description: "Used credit", AmountCents: 0}}, wantState: addonClosingState{invoices: 1, closedMonths: 1, nextNumber: 2, version: 1, creditMicrocents: "0"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			clock := parseBillingTime(t, "2026-12-01T00:00:00Z")
			handler := newBillingHandler(t, newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return clock }}), 5*time.Second, 8)
			if _, err := pool.Exec(ctx, tc.blockingSQL); err != nil {
				t.Fatal(err)
			}
			guard, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer guard.Rollback(context.Background())
			if _, err := guard.Exec(ctx, "SELECT pg_advisory_xact_lock(987002)"); err != nil {
				t.Fatal(err)
			}
			purchaseBody := `{"idempotency_key":"concurrent-addon","addon_name":"concurrency_pack","purchased_at":"` + tc.purchaseDate + `"}`
			purchaseDone, closeDone := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
			runPurchase := func() {
				purchaseDone <- financialRequest(t, handler, http.MethodPost, "/customers/acme/addons", purchaseBody)
			}
			runClose := func() {
				closeDone <- financialRequest(t, handler, http.MethodPost, "/customers/acme/invoices", `{"month":"`+tc.closingMonth+`"}`)
			}

			if tc.purchaseFirst {
				go runPurchase()
			} else {
				go runClose()
			}
			waitForClosingLock(t, ctx, pool, "advisory")
			if tc.purchaseFirst {
				go runClose()
			} else {
				go runPurchase()
			}
			waitForClosingLock(t, ctx, pool, "account")
			if err := guard.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			purchase, closed := <-purchaseDone, <-closeDone

			if purchase.Code != tc.wantPurchaseStatus {
				t.Fatalf("Concurrent purchase(%s): HTTP=%d body=%s; want %d", purchaseBody, purchase.Code, purchase.Body, tc.wantPurchaseStatus)
			}
			assertAddonClosingResponse(t, closed, tc.wantClose)
			assertAddonClosingState(t, pool, tc.wantClose.wantState)
			if _, err := pool.Exec(ctx, "DROP FUNCTION pause_addon_write() CASCADE"); err != nil {
				t.Fatal(err)
			}

			before := addonFinancialSnapshot(t, pool)
			replay := financialRequest(t, handler, http.MethodPost, "/customers/acme/addons", purchaseBody)
			if replay.Code != tc.wantPurchaseStatus || replay.Body.String() != purchase.Body.String() {
				t.Fatalf("Concurrent purchase retry: HTTP=%d body=%s; want %d body=%s", replay.Code, replay.Body, tc.wantPurchaseStatus, purchase.Body)
			}
			closeReplay := financialRequest(t, handler, http.MethodPost, "/customers/acme/invoices", `{"month":"`+tc.closingMonth+`"}`)
			if closeReplay.Code != tc.wantClose.wantStatus || closeReplay.Body.String() != closed.Body.String() {
				t.Fatalf("Concurrent closing retry(%s): HTTP=%d body=%s; want %d body=%s", tc.closingMonth, closeReplay.Code, closeReplay.Body, tc.wantClose.wantStatus, closed.Body)
			}
			if after := addonFinancialSnapshot(t, pool); after != before {
				t.Fatalf("Concurrent operation retries changed state: before=%s after=%s", before, after)
			}
		})
	}
}
