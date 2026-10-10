//go:build integration

package inbox_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/usage"
)

// testQuarantinedClosing checks errors discovered by the worker before closing.
// Every error stays unprocessed and leaves credit untouched while processed valid
// usage is billed. Closing and retries never process a released usage.
func testQuarantinedClosing(t *testing.T) {
	cases := []struct {
		name               string
		metric             string
		schemaVersion      int32
		periodEnd          string
		initialError       *string
		wantErrorText      string
		workerSteps        []bool
		wantTotalCents     int64
		wantGrossTicks     string
		wantCreditTicks    string
		wantRemainingTicks string
		wantRatings        int
	}{
		{name: "missing price discovered by worker", metric: "unpriced", schemaVersion: 1, periodEnd: "2026-10-10T13:00:00Z", wantErrorText: "P0 missing_valid_price:", workerSteps: []bool{true, true}, wantTotalCents: 3, wantGrossTicks: "5000000", wantCreditTicks: "2000000", wantRemainingTicks: "0", wantRatings: 1},
		{name: "unsupported schema discovered by worker", metric: "cpu_seconds", schemaVersion: 2, periodEnd: "2026-10-10T13:00:00Z", wantErrorText: "unsupported usage schema version", workerSteps: []bool{true, true}, wantTotalCents: 3, wantGrossTicks: "5000000", wantCreditTicks: "2000000", wantRemainingTicks: "0", wantRatings: 1},
		{name: "price crossing discovered by worker", metric: "cpu_seconds", schemaVersion: 1, periodEnd: "2026-10-16T00:00:00Z", wantErrorText: "usage interval crosses a price version boundary", workerSteps: []bool{true, true}, wantTotalCents: 3, wantGrossTicks: "5000000", wantCreditTicks: "2000000", wantRemainingTicks: "0", wantRatings: 1},
		{name: "preexisting arbitrary processing error is excluded", metric: "cpu_seconds", schemaVersion: 1, periodEnd: "2026-10-10T13:00:00Z", initialError: stringPointer("investigated processing failure"), wantErrorText: "investigated processing failure", workerSteps: []bool{true}, wantTotalCents: 3, wantGrossTicks: "5000000", wantCreditTicks: "2000000", wantRemainingTicks: "0", wantRatings: 1},
		{name: "even an empty nonnull error is excluded", metric: "cpu_seconds", schemaVersion: 1, periodEnd: "2026-10-10T13:00:00Z", initialError: stringPointer(""), wantErrorText: "", workerSteps: []bool{true}, wantTotalCents: 3, wantGrossTicks: "5000000", wantCreditTicks: "2000000", wantRemainingTicks: "0", wantRatings: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			if _, err := pool.Exec(ctx, "UPDATE customer_billing_state SET credit_balance_ticks=2000000 WHERE customer_id='cyberdyne'"); err != nil {
				t.Fatal(err)
			}
			transport := inbox.NewPostgres(pool, time.Second)
			bad := usage.Event{Source: "quarantine-close", EventID: "bad", SchemaVersion: tc.schemaVersion, CustomerID: "cyberdyne", SandboxID: "sandbox", Metric: tc.metric, PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, tc.periodEnd), Units: 1_000_000}
			good := usage.Event{Source: "quarantine-close", EventID: "good", SchemaVersion: 1, CustomerID: "cyberdyne", SandboxID: "sandbox", Metric: "cpu_seconds", PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z"), Units: 1_000_000}
			if err := transport.InsertBatch(ctx, []usage.Event{bad, good}, parseBillingTime(t, "2026-10-11T00:00:00Z")); err != nil {
				t.Fatal(err)
			}
			if tc.initialError != nil {
				if _, err := pool.Exec(ctx, "UPDATE usage_inbox SET processing_error=$1 WHERE source=$2 AND event_id=$3", *tc.initialError, bad.Source, bad.EventID); err != nil {
					t.Fatal(err)
				}
			}
			serverTime := parseBillingTime(t, "2026-12-01T00:00:00Z")
			store := billing.NewStoreWithClock(pool, func() time.Time { return serverTime })
			processUsageSteps(t, store, tc.workerSteps)

			invoice, err := store.CloseMonth(ctx, "cyberdyne", "2026-10")
			if err != nil {
				t.Fatalf("CloseMonth with %+v error=%v; want nil", bad, err)
			}
			if invoice.TotalCents != tc.wantTotalCents || invoice.GrossUsageTicks != tc.wantGrossTicks || invoice.CreditUsedTicks != tc.wantCreditTicks {
				t.Errorf("CloseMonth with %+v invoice=%+v; want total=%d gross=%s credit=%s", bad, invoice, tc.wantTotalCents, tc.wantGrossTicks, tc.wantCreditTicks)
			}
			var processed bool
			var processingError, remaining string
			var ratings int
			if err := pool.QueryRow(ctx, `SELECT i.processed_at IS NOT NULL,i.processing_error,
            (SELECT count(*) FROM usage_ratings),
            (SELECT credit_balance_ticks::text FROM customer_billing_state WHERE customer_id='cyberdyne')
            FROM usage_inbox i WHERE source=$1 AND event_id=$2`, bad.Source, bad.EventID).Scan(&processed, &processingError, &ratings, &remaining); err != nil {
				t.Fatal(err)
			}
			if processed || !strings.HasPrefix(processingError, tc.wantErrorText) {
				t.Errorf("Bad receipt processed=%t error=%q; want false and prefix=%q", processed, processingError, tc.wantErrorText)
			}

			if ratings != tc.wantRatings || remaining != tc.wantRemainingTicks {
				t.Errorf("Ratings=%d credit=%s; want %d and %s", ratings, remaining, tc.wantRatings, tc.wantRemainingTicks)
			}

			// Releasing usage does not make closing or its retry process it.
			if _, err := pool.Exec(ctx, "UPDATE usage_inbox SET processing_error=NULL WHERE source=$1 AND event_id=$2", bad.Source, bad.EventID); err != nil {
				t.Fatal(err)
			}
			retry, err := store.CloseMonth(ctx, "cyberdyne", "2026-10")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(retry, invoice) {
				t.Errorf("Invoice retry=%+v; want unchanged %+v", retry, invoice)
			}
		})
	}
}

// stringPointer represents an explicitly nonnull error, including the empty
// string, without hiding any scenario value or deriving a financial expectation.
func stringPointer(value string) *string { return &value }

// testClosingExclusionRecovery repairs an unpriced metric after issuance through
// operator SQL. The original month and invoice stay unchanged while the worker
// accounts recovered usage into the next eligible open month.
func testClosingExclusionRecovery(t *testing.T) {
	scenario := struct {
		periodStart       string
		periodEnd         string
		receivedAt        string
		repairSQL         string
		wantWork          bool
		wantUsageMonth    string
		wantBillingMonth  string
		wantOctoberTotal  int64
		wantNovemberTotal int64
	}{
		periodStart: "2026-10-10T12:00:00Z", periodEnd: "2026-10-10T13:00:00Z", receivedAt: "2026-10-11T00:00:00Z",
		repairSQL: "INSERT INTO price_versions VALUES ('operator-recovery',NULL,'unpriced',7,'2026-10-01T00:00:00Z')",
		wantWork:  true, wantUsageMonth: "2026-10", wantBillingMonth: "2026-11", wantOctoberTotal: 0, wantNovemberTotal: 7,
	}
	pool := billingDatabase(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "INSERT INTO metrics VALUES ('unpriced')"); err != nil {
		t.Fatal(err)
	}
	event := usage.Event{Source: "closing-recovery", EventID: "one", SchemaVersion: 1, CustomerID: "cyberdyne", SandboxID: "sandbox", Metric: "unpriced", PeriodStart: parseBillingTime(t, scenario.periodStart), PeriodEnd: parseBillingTime(t, scenario.periodEnd), Units: 1_000_000}
	if err := inbox.NewPostgres(pool, time.Second).InsertBatch(ctx, []usage.Event{event}, parseBillingTime(t, scenario.receivedAt)); err != nil {
		t.Fatal(err)
	}
	serverTime := parseBillingTime(t, "2026-12-01T00:00:00Z")
	store := billing.NewStoreWithClock(pool, func() time.Time { return serverTime })
	worked, err := store.ProcessBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if worked != scenario.wantWork {
		t.Errorf("Quarantine worked=%t; want %t", worked, scenario.wantWork)
	}
	october, err := store.CloseMonth(ctx, "cyberdyne", "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	if october.TotalCents != scenario.wantOctoberTotal {
		t.Errorf("October invoice total=%d; want %d", october.TotalCents, scenario.wantOctoberTotal)
	}

	if _, err := pool.Exec(ctx, scenario.repairSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE usage_inbox SET processing_error=NULL WHERE source=$1 AND event_id=$2", event.Source, event.EventID); err != nil {
		t.Fatal(err)
	}
	worked, err = store.ProcessBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if worked != scenario.wantWork {
		t.Errorf("Recovery worked=%t; want %t", worked, scenario.wantWork)
	}
	var usageMonth, billingMonth string
	if err := pool.QueryRow(ctx, `SELECT to_char(g.usage_month,'YYYY-MM'),to_char(g.billing_month,'YYYY-MM')
        FROM usage_ratings r JOIN rated_usage_groups g USING(group_id) WHERE source=$1 AND event_id=$2`, event.Source, event.EventID).Scan(&usageMonth, &billingMonth); err != nil {
		t.Fatal(err)
	}
	if usageMonth != scenario.wantUsageMonth || billingMonth != scenario.wantBillingMonth {
		t.Errorf("Recovery months=%s/%s; want %s/%s", usageMonth, billingMonth, scenario.wantUsageMonth, scenario.wantBillingMonth)
	}
	october, err = store.CloseMonth(ctx, "cyberdyne", "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	november, err := store.CloseMonth(ctx, "cyberdyne", "2026-11")
	if err != nil {
		t.Fatal(err)
	}
	if october.TotalCents != scenario.wantOctoberTotal || november.TotalCents != scenario.wantNovemberTotal {
		t.Errorf("Recovered invoices totals=%d/%d; want %d/%d", october.TotalCents, november.TotalCents, scenario.wantOctoberTotal, scenario.wantNovemberTotal)
	}
}
