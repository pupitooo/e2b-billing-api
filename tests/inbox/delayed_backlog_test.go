//go:build integration

package inbox_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/postgres"
	"e2b/billing-api/internal/usage"
)

// testDelayedPlatformBacklog distinguishes platform delay from worker delay and
// an unissued original month. Literal outcomes also show that a grant after
// accounting cannot retroactively pay old consumption or mutate a frozen invoice.
func testDelayedPlatformBacklog(t *testing.T) {
	cases := []struct {
		name                                                      string
		closedBeforeProcessing                                    []string
		grantBeforeProcessing                                     bool
		receiptTime, processingTime, targetMonth, wantDescription string
		wantTotalCents                                            int64
		wantAllocated, wantRemaining                              string
	}{
		{name: "closed origin routes platform backlog to receipt month", closedBeforeProcessing: []string{"2026-10"}, grantBeforeProcessing: true, receiptTime: "2026-11-15T00:00:00Z", processingTime: "2026-11-16T00:00:00Z", targetMonth: "2026-11", wantDescription: "Late usage from 2026-10", wantTotalCents: 10_000, wantAllocated: "10000000000", wantRemaining: "0"},
		{name: "worker delay beyond another closure advances billing again", closedBeforeProcessing: []string{"2026-10", "2026-11"}, grantBeforeProcessing: true, receiptTime: "2026-11-15T00:00:00Z", processingTime: "2026-12-15T00:00:00Z", targetMonth: "2026-12", wantDescription: "Late usage from 2026-10", wantTotalCents: 10_000, wantAllocated: "10000000000", wantRemaining: "0"},
		{name: "unissued origin retains its original billing month", grantBeforeProcessing: true, receiptTime: "2026-11-15T00:00:00Z", processingTime: "2026-11-16T00:00:00Z", targetMonth: "2026-10", wantDescription: "Usage", wantTotalCents: 10_000, wantAllocated: "10000000000", wantRemaining: "0"},
		{name: "grant after rating leaves old debit and invoice unchanged", closedBeforeProcessing: []string{"2026-10"}, grantBeforeProcessing: false, receiptTime: "2026-11-15T00:00:00Z", processingTime: "2026-11-16T00:00:00Z", targetMonth: "2026-11", wantDescription: "Late usage from 2026-10", wantTotalCents: 20_000, wantAllocated: "0", wantRemaining: "10000000000"},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			pool := billingDatabase(t)
			clock := parseBillingTime(t, "2027-01-01T00:00:00Z")
			store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return clock }})
			event := usage.Event{Source: "backlog", EventID: "october", SchemaVersion: 1, CustomerID: "acme", SandboxID: "delayed", Metric: "cpu_seconds", Units: 5_000_000_000, PeriodStart: parseBillingTime(t, "2026-10-31T23:59:00Z"), PeriodEnd: parseBillingTime(t, "2026-11-01T00:00:00Z")}
			var frozen []billing.Invoice
			// Ingest after the first close but before the second; November then publishes
			// while its receipt is pending, independently of the eventual worker clock.
			for index, month := range scenario.closedBeforeProcessing {
				invoice, err := store.CloseMonth(ctx, "acme", month)
				if err != nil {
					t.Fatal(err)
				}
				if invoice.TotalCents != 0 {
					t.Fatalf("pre-backlog invoice %s=%+v; want zero cents", month, invoice)
				}
				frozen = append(frozen, invoice)
				if index == 0 {
					if err := store.InsertBatch(ctx, []usage.Event{event}, parseBillingTime(t, scenario.receiptTime)); err != nil {
						t.Fatal(err)
					}
				}
			}
			if len(scenario.closedBeforeProcessing) == 0 {
				if err := store.InsertBatch(ctx, []usage.Event{event}, parseBillingTime(t, scenario.receiptTime)); err != nil {
					t.Fatal(err)
				}
			}
			grant := billing.CreditGrant{IdempotencyKey: "grant", AmountCents: 10_000}
			if scenario.grantBeforeProcessing {
				if err := store.GrantCredit(ctx, "acme", grant); err != nil {
					t.Fatal(err)
				}
			}
			clock = parseBillingTime(t, scenario.processingTime)

			processed, err := store.ProcessBatch(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !processed {
				t.Fatal("backlog ProcessBatch returned false; want one receipt processed")
			}
			if !scenario.grantBeforeProcessing {
				if err := store.GrantCredit(ctx, "acme", grant); err != nil {
					t.Fatal(err)
				}
			}
			clock = parseBillingTime(t, "2027-01-01T00:00:00Z")
			invoice, err := store.CloseMonth(ctx, "acme", scenario.targetMonth)
			if err != nil {
				t.Fatal(err)
			}
			if invoice.TotalCents != scenario.wantTotalCents || invoice.GrossUsageMicrocents != "20000000000" || invoice.CreditUsedMicrocents != scenario.wantAllocated || len(invoice.Lines) != 2 || invoice.Lines[0].Description != scenario.wantDescription || invoice.Lines[0].UsageMonth != "2026-10" || invoice.Lines[0].Units != "5000000000" {
				t.Fatalf("backlog %+v invoice=%+v; want total=%d allocation=%s description=%s", scenario, invoice, scenario.wantTotalCents, scenario.wantAllocated, scenario.wantDescription)
			}
			credit, err := store.Credit(ctx, "acme")
			if err != nil {
				t.Fatal(err)
			}
			if credit.BalanceMicrocents != scenario.wantRemaining || credit.PendingEvents != 0 || credit.ProcessingErrors != 0 {
				t.Fatalf("backlog credit=%+v; want balance=%s pending=0 errors=0", credit, scenario.wantRemaining)
			}
			for _, original := range frozen {
				actual, err := store.Invoice(ctx, "acme", original.Month)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(actual, original) {
					t.Fatalf("backlog changed closed invoice=%+v; want %+v", actual, original)
				}
			}
		})
	}
}
