//go:build integration

package inbox_test

import (
	"context"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/usage"
)

// testDelayedPriceProcessing follows two February 1 increments delivered before
// and after a February 2 price insertion. Both retain 100 cents; consumption at
// activation costs 200 cents. Closing before late delivery freezes the first bill.
func testDelayedPriceProcessing(t *testing.T) {
	cases := []struct {
		name                 string
		closeBeforeLate      bool
		lateReceivedAt       string
		activationReceivedAt string
		wantLateInvoice      int64
		wantCreditEntries    int
		wantFirstInvoice     int64
		wantLateBillingMonth string
		wantGrossTicks       string
		wantRatings          int
	}{
		{name: "late delivery into an open month", lateReceivedAt: "2026-02-10T00:00:00Z", activationReceivedAt: "2026-02-10T00:01:00Z", wantLateInvoice: 0, wantCreditEntries: 0, wantFirstInvoice: 400, wantLateBillingMonth: "2026-02", wantGrossTicks: "400000000", wantRatings: 3},
		{name: "late delivery after closure retains historical price", closeBeforeLate: true, lateReceivedAt: "2026-03-10T00:00:00Z", activationReceivedAt: "2026-03-10T00:01:00Z", wantLateInvoice: 300, wantCreditEntries: 0, wantFirstInvoice: 100, wantLateBillingMonth: "2026-03", wantGrossTicks: "400000000", wantRatings: 3},
	}
	steps := []struct {
		name        string
		periodStart string
		periodEnd   string
		receivedAt  string
		wantPriceID string
	}{
		{name: "A", periodStart: "2026-02-01T12:00:00Z", periodEnd: "2026-02-01T12:01:00Z", receivedAt: "2026-02-01T12:02:00Z", wantPriceID: "original-100"},
		{name: "B", periodStart: "2026-02-01T12:00:00Z", periodEnd: "2026-02-01T12:01:00Z", wantPriceID: "original-100"},
		{name: "C", periodStart: "2026-02-02T00:00:00Z", periodEnd: "2026-02-02T00:01:00Z", wantPriceID: "new-200"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			if _, err := pool.Exec(ctx, `INSERT INTO metrics VALUES ('activation-test');
            INSERT INTO price_versions VALUES ('original-100',NULL,'activation-test',100,'2026-01-01T00:00:00Z')`); err != nil {
				t.Fatal(err)
			}
			serverTime := parseBillingTime(t, "2026-02-02T00:00:00Z")
			store := billing.NewStoreWithClock(pool, func() time.Time { return serverTime })
			transport := inbox.NewPostgres(pool, time.Second)
			var generatedPriceID string
			for index, step := range steps {
				if index == 1 {
					price := billing.PriceInput{Metric: "activation-test", PricePerMillionCents: 200, EffectiveFrom: parseBillingTime(t, "2026-02-02T00:00:00Z")}
					created, err := store.CreatePrice(ctx, "new-200", price)
					if err != nil {
						t.Fatalf("Insert scheduled price %+v: %v", price, err)
					}
					generatedPriceID = created.ID
					if tc.closeBeforeLate {
						serverTime = parseBillingTime(t, "2026-03-01T00:00:00Z")
						invoice, err := store.CloseMonth(ctx, "cyberdyne", "2026-02")
						if err != nil {
							t.Fatal(err)
						}
						if invoice.TotalCents != tc.wantFirstInvoice {
							t.Errorf("First invoice=%d; want %d", invoice.TotalCents, tc.wantFirstInvoice)
						}
					}
				}
				event := usage.Event{Source: "activation", EventID: step.name, SchemaVersion: 1, CustomerID: "cyberdyne", SandboxID: "sandbox", Metric: "activation-test", PeriodStart: parseBillingTime(t, step.periodStart), PeriodEnd: parseBillingTime(t, step.periodEnd), Units: 1_000_000}
				receivedAt := step.receivedAt
				if index == 1 {
					receivedAt = tc.lateReceivedAt
				}
				if index == 2 {
					receivedAt = tc.activationReceivedAt
				}
				if err := transport.InsertBatch(ctx, []usage.Event{event}, parseBillingTime(t, receivedAt)); err != nil {
					t.Fatal(err)
				}
				worked, err := store.ProcessBatch(ctx)
				if err != nil {
					t.Fatalf("Process step %s: %v", step.name, err)
				}
				if !worked {
					t.Fatalf("Process step %s worked=false; want true", step.name)
				}
				var priceID, billingMonth string
				if err := pool.QueryRow(ctx, `SELECT g.price_version_id,to_char(g.billing_month,'YYYY-MM') FROM usage_ratings r JOIN rated_usage_groups g USING(group_id) WHERE source=$1 AND event_id=$2`, event.Source, event.EventID).Scan(&priceID, &billingMonth); err != nil {
					t.Fatal(err)
				}
				wantPriceID := step.wantPriceID
				if wantPriceID == "new-200" {
					wantPriceID = generatedPriceID
				}
				if priceID != wantPriceID {
					t.Errorf("Step %s price=%s; want %s", step.name, priceID, wantPriceID)
				}
				if index > 0 && billingMonth != tc.wantLateBillingMonth {
					t.Errorf("Late step billing month=%s; want %s", billingMonth, tc.wantLateBillingMonth)
				}
			}
			serverTime = parseBillingTime(t, "2026-04-01T00:00:00Z")
			invoice, err := store.CloseMonth(ctx, "cyberdyne", "2026-02")
			if err != nil {
				t.Fatal(err)
			}
			if invoice.TotalCents != tc.wantFirstInvoice {
				t.Errorf("Original invoice total=%d; want %d", invoice.TotalCents, tc.wantFirstInvoice)
			}
			lateInvoice, err := store.CloseMonth(ctx, "cyberdyne", "2026-03")
			if err != nil {
				t.Fatal(err)
			}
			if lateInvoice.TotalCents != tc.wantLateInvoice {
				t.Errorf("March invoice total=%d; want %d", lateInvoice.TotalCents, tc.wantLateInvoice)
			}

			var grossTicks string
			var ratings, creditEntries int
			if err := pool.QueryRow(ctx, `SELECT gross_charge_ticks::text,(SELECT count(*) FROM usage_ratings),(SELECT count(*) FROM credit_entries) FROM monthly_usage WHERE customer_id='cyberdyne' AND usage_month='2026-02-01'`).Scan(&grossTicks, &ratings, &creditEntries); err != nil {
				t.Fatal(err)
			}
			if grossTicks != tc.wantGrossTicks || ratings != tc.wantRatings || creditEntries != tc.wantCreditEntries {
				t.Errorf("Monthly gross=%s ratings=%d credit entries=%d; want %s,%d,%d", grossTicks, ratings, creditEntries, tc.wantGrossTicks, tc.wantRatings, tc.wantCreditEntries)
			}
		})
	}
}
