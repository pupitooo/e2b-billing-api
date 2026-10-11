//go:build integration

package inbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/postgres"
	"e2b/billing-api/internal/usage"
)

// testBatchFinancialState conflicts after a new row in a batch that includes an
// already rated receipt. Rollback preserves all projections; a valid retry then
// rates the new row exactly once and leaves the original financial effect intact.
func testBatchFinancialState(t *testing.T) {
	scenario := struct {
		customer, receivedAt                              string
		grantCents, originalUnits, changedUnits, newUnits int64
		wantError                                         bool
		wantState                                         resourceState
		wantGross                                         string
		wantLedgerEntries                                 int
	}{customer: "acme", receivedAt: "2026-10-11T00:00:00Z", grantCents: 20,
		originalUnits: 1_000_000, changedUnits: 1_000_001, newUnits: 1_000_000,
		wantError: true, wantState: resourceState{prices: 3, operations: 1, ratings: 2, stateVersion: 3, creditMicrocents: "12000000"},
		wantGross: "8000000", wantLedgerEntries: 3,
	}
	pool := billingDatabase(t)
	ctx := context.Background()
	store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return parseBillingTime(t, scenario.receivedAt) }})
	if err := store.GrantCredit(ctx, scenario.customer, billing.CreditGrant{IdempotencyKey: "grant", AmountCents: scenario.grantCents}); err != nil {
		t.Fatal(err)
	}
	original := usage.Event{Source: "financial-batch", EventID: "original", SchemaVersion: 1, CustomerID: scenario.customer, SandboxID: "sandbox", Metric: "cpu_seconds", PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z"), Units: scenario.originalUnits}
	received := parseBillingTime(t, scenario.receivedAt)
	if err := store.InsertBatch(ctx, []usage.Event{original}, received); err != nil {
		t.Fatal(err)
	}
	processUsageSteps(t, store, []bool{true, false})
	changed := original
	changed.Units = scenario.changedUnits
	newEvent := original
	newEvent.EventID, newEvent.Units = "000-new-before-conflict", scenario.newUnits
	before := addonFinancialSnapshot(t, pool)

	err := store.InsertBatch(ctx, []usage.Event{newEvent, changed}, received.Add(time.Hour))

	var conflict *postgres.ConflictError
	if (err != nil) != scenario.wantError || !errors.As(err, &conflict) {
		t.Fatalf("InsertBatch(new=%+v changed=%+v): error=%v; want identity conflict", newEvent, changed, err)
	}
	if conflict.Source != original.Source || conflict.EventID != original.EventID {
		t.Fatalf("Conflict=%+v; want source=%s event=%s", conflict, original.Source, original.EventID)
	}
	if after := addonFinancialSnapshot(t, pool); after != before {
		t.Fatalf("Conflicting batch changed financial state: before=%s after=%s", before, after)
	}

	if err := store.InsertBatch(ctx, []usage.Event{newEvent, original}, received.Add(time.Hour)); err != nil {
		t.Fatalf("Valid neighboring retry: %v", err)
	}
	processUsageSteps(t, store, []bool{true, false})
	assertResourceState(t, pool, scenario.wantState)
	var gross string
	var entries int
	if err := pool.QueryRow(ctx, `SELECT gross_charge_microcents::text,(SELECT count(*) FROM credit_entries) FROM monthly_usage WHERE customer_id=$1`, scenario.customer).Scan(&gross, &entries); err != nil {
		t.Fatal(err)
	}
	if gross != scenario.wantGross || entries != scenario.wantLedgerEntries {
		t.Fatalf("Recovered batch gross=%s ledger=%d; want %s %d", gross, entries, scenario.wantGross, scenario.wantLedgerEntries)
	}
}
