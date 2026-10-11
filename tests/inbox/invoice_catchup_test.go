//go:build integration

package inbox_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/postgres"
)

// TestCatchUpInvoices closes missing months in order, including empty months and
// known unprocessed obligations, while rejected commands leave all state intact.
func TestCatchUpInvoices(t *testing.T) {
	t.Run("atomic failure and successful retry", testCatchupRollback)
	t.Run("long gaps recover through consecutive bounded commands", testCatchupSplit)
	t.Run("concurrent command identities", testCatchupConcurrency)
	t.Run("customer and command scopes", testCatchupScopes)
	t.Run("single-month closing competes with recovery", testCatchupClosingConcurrency)
	t.Run("worker cutoff preserves every pending charge", testCatchupPending)
	t.Run("first obligation uses UTC month boundaries", testCatchupFirstMonthUTC)
	t.Run("durable command replay after later invoices", testCatchupReplay)
	cases := []struct {
		name, customer, key, through, now, firstClose, purchased, cancelled, receiptMonth string
		process                                                                           bool
		wantMonths, wantNumbers                                                           []string
		wantTotals                                                                        []int64
		wantError                                                                         error
		wantField                                                                         string
		wantInvoiceCount                                                                  int
	}{
		{name: "missed November includes empty December", customer: "acme", key: "run", through: "2026-12", now: "2027-01-01T00:00:00Z", firstClose: "2026-10", wantMonths: []string{"2026-11", "2026-12"}, wantNumbers: []string{"ACME-0002", "ACME-0003"}, wantTotals: []int64{0, 0}, wantInvoiceCount: 3},
		{name: "add-on obligations establish first month", customer: "acme", key: "run", through: "2026-12", now: "2027-01-01T00:00:00Z", purchased: "2026-10-31T00:00:00Z", wantMonths: []string{"2026-10", "2026-11", "2026-12"}, wantNumbers: []string{"ACME-0001", "ACME-0002", "ACME-0003"}, wantTotals: []int64{2000, 2000, 2000}, wantInvoiceCount: 3},
		{name: "cancelled month billed but later months empty", customer: "acme", key: "run", through: "2026-12", now: "2027-01-01T00:00:00Z", purchased: "2026-10-01T00:00:00Z", cancelled: "2026-11-30T00:00:00Z", wantMonths: []string{"2026-10", "2026-11", "2026-12"}, wantNumbers: []string{"ACME-0001", "ACME-0002", "ACME-0003"}, wantTotals: []int64{2000, 2000, 0}, wantInvoiceCount: 3},
		{name: "first run discovers oldest rated usage", customer: "acme", key: "run", through: "2026-12", now: "2027-01-01T00:00:00Z", receiptMonth: "2026-10", process: true, wantMonths: []string{"2026-10", "2026-11", "2026-12"}, wantNumbers: []string{"ACME-0001", "ACME-0002", "ACME-0003"}, wantTotals: []int64{400, 0, 0}, wantInvoiceCount: 3},
		{name: "first run discovers pending receipt without draining", customer: "acme", key: "run", through: "2026-12", now: "2027-01-01T00:00:00Z", receiptMonth: "2026-10", wantMonths: []string{"2026-10", "2026-11", "2026-12"}, wantNumbers: []string{"ACME-0001", "ACME-0002", "ACME-0003"}, wantTotals: []int64{0, 0, 0}, wantInvoiceCount: 3},
		{name: "no obligations starts at target", customer: "acme", key: "run", through: "2026-12", now: "2027-01-01T00:00:00Z", wantMonths: []string{"2026-12"}, wantNumbers: []string{"ACME-0001"}, wantTotals: []int64{0}, wantInvoiceCount: 1},
		{name: "existing target returns its original snapshot", customer: "acme", key: "run", through: "2026-10", now: "2027-01-01T00:00:00Z", firstClose: "2026-10", wantMonths: []string{"2026-10"}, wantNumbers: []string{"ACME-0001"}, wantTotals: []int64{0}, wantInvoiceCount: 1},
		{name: "cannot reopen before the first closed month", customer: "acme", key: "run", through: "2026-09", now: "2027-01-01T00:00:00Z", firstClose: "2026-10", wantError: billing.ErrConflict, wantInvoiceCount: 1},
		{name: "unfinished target rejects entire missing range", customer: "acme", key: "run", through: "2026-12", now: "2026-12-31T23:59:59.999999Z", firstClose: "2026-10", wantField: "through_month", wantInvoiceCount: 1},
		{name: "blank command key", customer: "acme", key: "", through: "2026-12", now: "2027-01-01T00:00:00Z", wantField: "idempotency_key"},
		{name: "invalid target month", customer: "acme", key: "run", through: "2026-13", now: "2027-01-01T00:00:00Z", wantField: "through_month"},
		{name: "first supported year", customer: "acme", key: "run", through: "1000-01", now: "2027-01-01T00:00:00Z", wantMonths: []string{"1000-01"}, wantNumbers: []string{"ACME-0001"}, wantTotals: []int64{0}, wantInvoiceCount: 1},
		{name: "last completed month in supported years", customer: "acme", key: "run", through: "9999-11", now: "9999-12-01T00:00:00Z", wantMonths: []string{"9999-11"}, wantNumbers: []string{"ACME-0001"}, wantTotals: []int64{0}, wantInvoiceCount: 1},
		{name: "unsupported target year", customer: "acme", key: "run", through: "0999-12", now: "2027-01-01T00:00:00Z", wantField: "through_month"},
		{name: "unknown customer", customer: "missing", key: "run", through: "2026-12", now: "2027-01-01T00:00:00Z", wantError: billing.ErrNotFound},
		{name: "twelve missing months accepted", customer: "acme", key: "run", through: "2027-10", now: "2027-12-01T00:00:00Z", firstClose: "2026-10", wantMonths: []string{"2026-11", "2026-12", "2027-01", "2027-02", "2027-03", "2027-04", "2027-05", "2027-06", "2027-07", "2027-08", "2027-09", "2027-10"}, wantNumbers: []string{"ACME-0002", "ACME-0003", "ACME-0004", "ACME-0005", "ACME-0006", "ACME-0007", "ACME-0008", "ACME-0009", "ACME-0010", "ACME-0011", "ACME-0012", "ACME-0013"}, wantTotals: []int64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, wantInvoiceCount: 13},
		{name: "thirteenth missing month requires split without effects", customer: "acme", key: "run", through: "2027-11", now: "2027-12-01T00:00:00Z", firstClose: "2026-10", wantField: "through_month", wantInvoiceCount: 1},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return parseBillingTime(t, scenario.now) }})
			if scenario.purchased != "" {
				if _, err := store.PurchaseAddon(ctx, "acme", "purchase", billing.AddonPurchase{AddonName: "concurrency_pack", PurchasedAt: parseBillingTime(t, scenario.purchased)}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.cancelled != "" {
				if _, err := store.CancelAddon(ctx, "acme", "cancel", billing.AddonCancellation{SubscriptionID: "subscription/acme/concurrency_pack", CancelledAt: parseBillingTime(t, scenario.cancelled)}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.receiptMonth != "" {
				insertMeasuredHours(t, store, "acme", []measuredHour{{eventID: "known", start: scenario.receiptMonth + "-10T12:00:00Z", units: 100_000_000}}, "2026-12-31T00:00:00Z")
				if scenario.process {
					processUsageSteps(t, store, []bool{true, false})
				}
			}
			if scenario.firstClose != "" {
				if _, err := store.CloseMonth(ctx, "acme", scenario.firstClose); err != nil {
					t.Fatal(err)
				}
			}
			before := addonFinancialSnapshot(t, pool)

			result, err := store.CatchUpInvoices(ctx, scenario.customer, scenario.key, scenario.through)
			if scenario.wantField != "" {
				var validation *billing.ValidationError
				if !errors.As(err, &validation) || validation.Field != scenario.wantField {
					t.Fatalf("CatchUpInvoices(%s,%s,%s) error=%v; want field=%s", scenario.customer, scenario.key, scenario.through, err, scenario.wantField)
				}
			} else if !errors.Is(err, scenario.wantError) {
				t.Fatalf("CatchUpInvoices(%s,%s,%s) error=%v; want %v", scenario.customer, scenario.key, scenario.through, err, scenario.wantError)
			}
			if err != nil {
				if after := addonFinancialSnapshot(t, pool); after != before {
					t.Fatalf("rejected catch-up changed state: before=%s after=%s", before, after)
				}
			} else {
				var months, numbers []string
				var totals []int64
				for _, invoice := range result.Invoices {
					months = append(months, invoice.Month)
					numbers = append(numbers, invoice.Number)
					totals = append(totals, invoice.TotalCents)
					loaded, err := store.Invoice(ctx, scenario.customer, invoice.Month)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(loaded, invoice) {
						t.Fatalf("catch-up invoice %s differs from stored snapshot", invoice.Month)
					}
				}
				if result.CustomerID != scenario.customer || result.ThroughMonth != scenario.through || !reflect.DeepEqual(months, scenario.wantMonths) || !reflect.DeepEqual(numbers, scenario.wantNumbers) || !reflect.DeepEqual(totals, scenario.wantTotals) {
					t.Fatalf("CatchUpInvoices(%s) result=%+v; want months=%v numbers=%v totals=%v", scenario.through, result, scenario.wantMonths, scenario.wantNumbers, scenario.wantTotals)
				}
			}
			var count int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM invoices").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != scenario.wantInvoiceCount {
				t.Fatalf("CatchUpInvoices(%s) invoices=%d; want %d", scenario.through, count, scenario.wantInvoiceCount)
			}
		})
	}
}

// testCatchupReplay freezes the original returned range across restarts and
// later closures; changed content conflicts, while a new key reads one snapshot.
func testCatchupReplay(t *testing.T) {
	scenario := struct {
		first, through, later, key string
		wantMonths                 []string
		wantNewKeyMonths           []string
		wantChangedError           error
		wantInvoices               int
	}{first: "2026-10", through: "2026-12", later: "2027-01", key: "recover", wantMonths: []string{"2026-11", "2026-12"}, wantNewKeyMonths: []string{"2026-12"}, wantChangedError: billing.ErrConflict, wantInvoices: 4}
	pool := billingDatabase(t)
	ctx := context.Background()
	config := postgres.Config{Now: func() time.Time { return parseBillingTime(t, "2027-02-01T00:00:00Z") }}
	store := newPostgresStore(t, pool, config)
	if _, err := store.CloseMonth(ctx, "acme", scenario.first); err != nil {
		t.Fatal(err)
	}
	original, err := store.CatchUpInvoices(ctx, "acme", scenario.key, scenario.through)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CloseMonth(ctx, "acme", scenario.later); err != nil {
		t.Fatal(err)
	}
	before := addonFinancialSnapshot(t, pool)
	replay, err := newPostgresStore(t, pool, config).CatchUpInvoices(ctx, "acme", scenario.key, scenario.through)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replay, original) || addonFinancialSnapshot(t, pool) != before {
		t.Fatal("catch-up replay changed snapshots or financial state")
	}
	_, err = store.CatchUpInvoices(ctx, "acme", scenario.key, scenario.later)
	if !errors.Is(err, scenario.wantChangedError) {
		t.Fatalf("changed through error=%v; want %v", err, scenario.wantChangedError)
	}
	if addonFinancialSnapshot(t, pool) != before {
		t.Fatal("changed catch-up affected state")
	}
	other, err := store.CatchUpInvoices(ctx, "acme", "new-key", scenario.through)
	if err != nil {
		t.Fatal(err)
	}
	var months []string
	for _, invoice := range original.Invoices {
		months = append(months, invoice.Month)
	}
	var newMonths []string
	for _, invoice := range other.Invoices {
		newMonths = append(newMonths, invoice.Month)
	}
	if !reflect.DeepEqual(months, scenario.wantMonths) || !reflect.DeepEqual(newMonths, scenario.wantNewKeyMonths) {
		t.Fatalf("ranges=%v,%v; want %v,%v", months, newMonths, scenario.wantMonths, scenario.wantNewKeyMonths)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM invoices").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != scenario.wantInvoices {
		t.Fatalf("replay invoices=%d; want %d", count, scenario.wantInvoices)
	}
}
