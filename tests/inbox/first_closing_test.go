//go:build integration

package inbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/usage"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testClosingFirstMonthBoundary checks the UTC month boundary of existing usage
// under both session zones. The close never processes that usage or changes credit.
func testClosingFirstMonthBoundary(t *testing.T) {
	cases := []struct {
		name, timeZone, start, end, receivedAt, closingMonth string
		units                                                int64
		wantError                                            error
		wantInvoices, wantRatings, wantProcessed             int
		wantNextNumber, wantVersion                          int64
		wantCredit                                           string
	}{
		{name: "UTC earlier October cannot be skipped", timeZone: "UTC", start: "2026-10-31T23:59:00Z", end: "2026-11-01T00:00:00Z", receivedAt: "2026-11-01T00:00:01Z", closingMonth: "2026-11", units: 1_000_000, wantError: billing.ErrConflict, wantInvoices: 0, wantRatings: 0, wantProcessed: 0, wantNextNumber: 1, wantVersion: 0, wantCredit: "0"},
		{name: "Shanghai earlier October cannot be skipped", timeZone: "Asia/Shanghai", start: "2026-10-31T23:59:00Z", end: "2026-11-01T00:00:00Z", receivedAt: "2026-11-01T00:00:01Z", closingMonth: "2026-11", units: 1_000_000, wantError: billing.ErrConflict, wantInvoices: 0, wantRatings: 0, wantProcessed: 0, wantNextNumber: 1, wantVersion: 0, wantCredit: "0"},
		{name: "UTC current November stays pending without blocking", timeZone: "UTC", start: "2026-11-01T00:00:00Z", end: "2026-11-01T00:01:00Z", receivedAt: "2026-11-01T00:01:01Z", closingMonth: "2026-11", units: 1_000_000, wantError: nil, wantInvoices: 1, wantRatings: 0, wantProcessed: 0, wantNextNumber: 2, wantVersion: 1, wantCredit: "0"},
		{name: "Shanghai current November stays pending without blocking", timeZone: "Asia/Shanghai", start: "2026-11-01T00:00:00Z", end: "2026-11-01T00:01:00Z", receivedAt: "2026-11-01T00:01:01Z", closingMonth: "2026-11", units: 1_000_000, wantError: nil, wantInvoices: 1, wantRatings: 0, wantProcessed: 0, wantNextNumber: 2, wantVersion: 1, wantCredit: "0"},
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
			var zone string
			if err := pool.QueryRow(ctx, "SHOW timezone").Scan(&zone); err != nil {
				t.Fatal(err)
			}
			if zone != tc.timeZone {
				t.Fatalf("Closing session timezone=%s; want %s", zone, tc.timeZone)
			}
			store := billing.NewStoreWithClock(pool, func() time.Time { return parseBillingTime(t, "2026-12-01T00:00:00Z") })
			event := usage.Event{Source: "first-closing", EventID: "one", SchemaVersion: 1, CustomerID: "acme", SandboxID: "sandbox", Metric: "cpu_seconds", PeriodStart: parseBillingTime(t, tc.start), PeriodEnd: parseBillingTime(t, tc.end), Units: tc.units}
			if err := inbox.NewPostgres(pool, time.Second).InsertBatch(ctx, []usage.Event{event}, parseBillingTime(t, tc.receivedAt)); err != nil {
				t.Fatal(err)
			}

			_, err = store.CloseMonth(ctx, "acme", tc.closingMonth)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("CloseMonth(%s) usage=%+v timezone=%s error=%v; want %v", tc.closingMonth, event, tc.timeZone, err, tc.wantError)
			}

			var invoices, ratings, processed int
			var next, version int64
			var credit string
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM invoices),(SELECT count(*) FROM usage_ratings),
                (SELECT count(*) FROM usage_inbox WHERE processed_at IS NOT NULL),next_invoice_number,state_version,credit_balance_ticks::text
                FROM customer_billing_state WHERE customer_id='acme'`).Scan(&invoices, &ratings, &processed, &next, &version, &credit); err != nil {
				t.Fatal(err)
			}
			if invoices != tc.wantInvoices || ratings != tc.wantRatings || processed != tc.wantProcessed || next != tc.wantNextNumber || version != tc.wantVersion || credit != tc.wantCredit {
				t.Errorf("First closing invoices=%d ratings=%d processed=%d next=%d version=%d credit=%s; want %+v", invoices, ratings, processed, next, version, credit, tc)
			}
		})
	}
}
