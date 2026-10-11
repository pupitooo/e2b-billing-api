//go:build integration

package inbox_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/postgres"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testCatchupRollback fails the second invoice after the first was constructed;
// invoices, group freezes, numbers, versions and command history all roll back.
func testCatchupRollback(t *testing.T) {
	scenario := struct {
		through, key string
		creditCents  int64
		usage        []measuredHour
		wantSQLState string
		wantInvoices []invoiceExpectation
		wantCount    int
	}{through: "2026-11", key: "run", creditCents: 500, usage: []measuredHour{{eventID: "oct", start: "2026-10-10T12:00:00Z", units: 100_000_000}}, wantSQLState: "P0001", wantCount: 2, wantInvoices: []invoiceExpectation{
		{month: "2026-10", number: "ACME-0001", totalCents: 2000, lineCents: []int64{400, 2000, -400}, grossMicrocents: "400000000", creditMicrocents: "400000000", remainingCredit: "100000000"},
		{month: "2026-11", number: "ACME-0002", totalCents: 2000, lineCents: []int64{2000, 0}, grossMicrocents: "0", creditMicrocents: "0", remainingCredit: "100000000"}}}
	pool := billingDatabase(t)
	ctx := context.Background()
	store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return parseBillingTime(t, "2026-12-01T00:00:00Z") }})
	if err := store.GrantCredit(ctx, "acme", billing.CreditGrant{IdempotencyKey: "credit", AmountCents: scenario.creditCents}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PurchaseAddon(ctx, "acme", "addon", billing.AddonPurchase{AddonName: "concurrency_pack", PurchasedAt: parseBillingTime(t, "2026-10-01T00:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	insertMeasuredHours(t, store, "acme", scenario.usage, "2026-10-31T00:00:00Z")
	processUsageSteps(t, store, []bool{true, false})
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_second_invoice() RETURNS trigger LANGUAGE plpgsql AS $f$
 BEGIN IF NEW.billing_month = DATE '2026-11-01' THEN RAISE EXCEPTION 'injected second invoice failure'; END IF; RETURN NEW; END; $f$;
 CREATE TRIGGER fail_second BEFORE INSERT ON invoices FOR EACH ROW EXECUTE FUNCTION fail_second_invoice()`); err != nil {
		t.Fatal(err)
	}
	before := addonFinancialSnapshot(t, pool)

	_, err := store.CatchUpInvoices(ctx, "acme", scenario.key, scenario.through)
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) || databaseError.Code != scenario.wantSQLState {
		t.Fatalf("CatchUpInvoices through=%s error=%v; want SQLSTATE %s", scenario.through, err, scenario.wantSQLState)
	}
	if after := addonFinancialSnapshot(t, pool); after != before {
		t.Fatalf("failed second invoice changed state: before=%s after=%s", before, after)
	}
	if _, err := pool.Exec(ctx, "DROP FUNCTION fail_second_invoice() CASCADE"); err != nil {
		t.Fatal(err)
	}

	result, err := store.CatchUpInvoices(ctx, "acme", scenario.key, scenario.through)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Invoices) != scenario.wantCount {
		t.Fatalf("retry invoices=%d; want %d", len(result.Invoices), scenario.wantCount)
	}
	for index, want := range scenario.wantInvoices {
		assertInvoiceExpectation(t, store, result.Invoices[index], want)
	}
}

// testCatchupPending compares both worker cutoffs. An unprocessed old receipt
// remains outside every frozen snapshot and is billed once in the next open month.
func testCatchupPending(t *testing.T) {
	cases := []struct {
		name                    string
		processBefore           bool
		wantCatchupTotals       []int64
		wantJanuary             invoiceExpectation
		wantRatings, wantFrozen int
		wantPending             int64
	}{
		{name: "worker commits first and original month retains charge", processBefore: true, wantCatchupTotals: []int64{400, 0, 0}, wantJanuary: invoiceExpectation{month: "2027-01", number: "ACME-0004", totalCents: 0, lineCents: []int64{0}, grossMicrocents: "0", creditMicrocents: "0", remainingCredit: "0"}, wantRatings: 1, wantFrozen: 1, wantPending: 0},
		{name: "catch-up commits first and receipt routes after entire range", processBefore: false, wantCatchupTotals: []int64{0, 0, 0}, wantJanuary: invoiceExpectation{month: "2027-01", number: "ACME-0004", totalCents: 400, lineCents: []int64{400, 0}, grossMicrocents: "400000000", creditMicrocents: "0", remainingCredit: "0"}, wantRatings: 1, wantFrozen: 1, wantPending: 1},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return parseBillingTime(t, "2027-02-01T00:00:00Z") }})
			insertMeasuredHours(t, store, "acme", []measuredHour{{eventID: "backlog", start: "2026-10-10T12:00:00Z", units: 100_000_000}}, "2026-12-31T00:00:00Z")
			if scenario.processBefore {
				processUsageSteps(t, store, []bool{true, false})
			}

			run, err := store.CatchUpInvoices(ctx, "acme", "recover", "2026-12")
			if err != nil {
				t.Fatal(err)
			}
			var totals []int64
			for _, invoice := range run.Invoices {
				totals = append(totals, invoice.TotalCents)
			}
			if !reflect.DeepEqual(totals, scenario.wantCatchupTotals) {
				t.Fatalf("cutoff processBefore=%t totals=%v; want %v", scenario.processBefore, totals, scenario.wantCatchupTotals)
			}
			snapshot, err := store.Credit(ctx, "acme")
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.PendingEvents != scenario.wantPending {
				t.Fatalf("catch-up pending=%d; want %d", snapshot.PendingEvents, scenario.wantPending)
			}
			if !scenario.processBefore {
				processUsageSteps(t, store, []bool{true, false})
			}
			january, err := store.CloseMonth(ctx, "acme", "2027-01")
			if err != nil {
				t.Fatal(err)
			}
			assertInvoiceExpectation(t, store, january, scenario.wantJanuary)
			for _, original := range run.Invoices {
				loaded, err := store.Invoice(ctx, "acme", original.Month)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(loaded, original) {
					t.Fatalf("worker changed catch-up snapshot %s", original.Month)
				}
			}
			var ratings, frozen int
			if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM usage_ratings),(SELECT count(*) FROM invoiced_usage_groups)").Scan(&ratings, &frozen); err != nil {
				t.Fatal(err)
			}
			if ratings != scenario.wantRatings || frozen != scenario.wantFrozen {
				t.Fatalf("cutoff ratings=%d frozen=%d; want %d,%d", ratings, frozen, scenario.wantRatings, scenario.wantFrozen)
			}
		})
	}
}

// testCatchupConcurrency issues competing keys in isolated schemas. Account and
// command locks permit one publication range, with each key replaying its result.
func testCatchupConcurrency(t *testing.T) {
	cases := []struct {
		name                         string
		keys                         []string
		wantOperations, wantInvoices int
		wantRangeLengths             []int
		wantIdentical                bool
	}{
		{name: "same key gives the same full range twice", keys: []string{"recover", "recover"}, wantOperations: 1, wantInvoices: 3, wantRangeLengths: []int{2, 2}, wantIdentical: true},
		{name: "different keys serialize without duplicate invoices", keys: []string{"first", "second"}, wantOperations: 2, wantInvoices: 3, wantRangeLengths: []int{1, 2}},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return parseBillingTime(t, "2027-01-01T00:00:00Z") }})
			if _, err := store.CloseMonth(ctx, "acme", "2026-10"); err != nil {
				t.Fatal(err)
			}
			results := make([]billing.InvoiceRun, len(scenario.keys))
			failures := make([]error, len(scenario.keys))
			start := make(chan struct{})
			var group sync.WaitGroup
			for index, key := range scenario.keys {
				group.Add(1)
				go func() {
					defer group.Done()
					<-start
					results[index], failures[index] = store.CatchUpInvoices(ctx, "acme", key, "2026-12")
				}()
			}
			close(start)
			group.Wait()
			for index, err := range failures {
				if err != nil {
					t.Fatalf("concurrent key=%s error=%v; want success", scenario.keys[index], err)
				}
			}
			lengths := []int{len(results[0].Invoices), len(results[1].Invoices)}
			if lengths[0] > lengths[1] {
				lengths[0], lengths[1] = lengths[1], lengths[0]
			}
			if !reflect.DeepEqual(lengths, scenario.wantRangeLengths) {
				t.Fatalf("concurrent range lengths=%v; want %v", lengths, scenario.wantRangeLengths)
			}
			if scenario.wantIdentical && !reflect.DeepEqual(results[0], results[1]) {
				t.Fatal("same key returned different invoice ranges")
			}
			for index, key := range scenario.keys {
				replay, err := store.CatchUpInvoices(ctx, "acme", key, "2026-12")
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(replay, results[index]) {
					t.Fatalf("concurrent key=%s replay differs", key)
				}
			}
			var operations, invoices int
			if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM api_idempotency_operations WHERE operation_scope='invoice-catch-up/acme'),(SELECT count(*) FROM invoices)").Scan(&operations, &invoices); err != nil {
				t.Fatal(err)
			}
			if operations != scenario.wantOperations || invoices != scenario.wantInvoices {
				t.Fatalf("concurrent operations=%d invoices=%d; want %d,%d", operations, invoices, scenario.wantOperations, scenario.wantInvoices)
			}
		})
	}
}

// testCatchupSplit rejects an oversized first run without effects, then bills
// all thirteen add-on months through two bounded commands with ordinary numbering.
func testCatchupSplit(t *testing.T) {
	scenario := struct {
		purchase, tooFar, first, last string
		wantField                     string
		wantLengths                   []int
		wantTotal                     int64
		wantInvoices, wantOperations  int
		wantLastNumber                string
	}{purchase: "2026-10-01T00:00:00Z", tooFar: "2027-10", first: "2027-09", last: "2027-10", wantField: "through_month", wantLengths: []int{12, 1}, wantTotal: 26_000, wantInvoices: 13, wantOperations: 2, wantLastNumber: "ACME-0013"}
	pool := billingDatabase(t)
	ctx := context.Background()
	store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return parseBillingTime(t, "2027-11-01T00:00:00Z") }})
	if _, err := store.PurchaseAddon(ctx, "acme", "addon", billing.AddonPurchase{AddonName: "concurrency_pack", PurchasedAt: parseBillingTime(t, scenario.purchase)}); err != nil {
		t.Fatal(err)
	}
	before := addonFinancialSnapshot(t, pool)

	_, err := store.CatchUpInvoices(ctx, "acme", "failed", scenario.tooFar)
	var validation *billing.ValidationError
	if !errors.As(err, &validation) || validation.Field != scenario.wantField {
		t.Fatalf("oversized first run error=%v; want field=%s", err, scenario.wantField)
	}
	if addonFinancialSnapshot(t, pool) != before {
		t.Fatal("oversized first run affected financial state")
	}
	first, err := store.CatchUpInvoices(ctx, "acme", "first", scenario.first)
	if err != nil {
		t.Fatal(err)
	}
	last, err := store.CatchUpInvoices(ctx, "acme", "last", scenario.last)
	if err != nil {
		t.Fatal(err)
	}
	lengths := []int{len(first.Invoices), len(last.Invoices)}
	if !reflect.DeepEqual(lengths, scenario.wantLengths) {
		t.Fatalf("split lengths=%v; want %v", lengths, scenario.wantLengths)
	}
	if last.Invoices[0].Number != scenario.wantLastNumber {
		t.Fatalf("split last number=%s; want %s", last.Invoices[0].Number, scenario.wantLastNumber)
	}
	var total int64
	var invoices, operations int
	if err := pool.QueryRow(ctx, "SELECT sum(total_cents),count(*),(SELECT count(*) FROM api_idempotency_operations WHERE operation_scope='invoice-catch-up/acme') FROM invoices").Scan(&total, &invoices, &operations); err != nil {
		t.Fatal(err)
	}
	if total != scenario.wantTotal || invoices != scenario.wantInvoices || operations != scenario.wantOperations {
		t.Fatalf("split total=%d invoices=%d keys=%d; want %d,%d,%d", total, invoices, operations, scenario.wantTotal, scenario.wantInvoices, scenario.wantOperations)
	}
}

// testCatchupScopes shares a human-readable key across customers and credit
// commands, verifying independent invoice ranges and unchanged granted balance.
func testCatchupScopes(t *testing.T) {
	scenario := struct {
		key            string
		creditCents    int64
		steps          []struct{ customer, through, wantNumber string }
		wantBalance    string
		wantOperations int
	}{key: "shared", creditCents: 100, steps: []struct{ customer, through, wantNumber string }{
		{customer: "acme", through: "2026-10", wantNumber: "ACME-0001"},
		{customer: "cyberdyne", through: "2026-11", wantNumber: "CYBERDYNE-0001"}}, wantBalance: "100000000", wantOperations: 3}
	pool := billingDatabase(t)
	ctx := context.Background()
	store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return parseBillingTime(t, "2026-12-01T00:00:00Z") }})
	if err := store.GrantCredit(ctx, "acme", billing.CreditGrant{IdempotencyKey: scenario.key, AmountCents: scenario.creditCents}); err != nil {
		t.Fatal(err)
	}
	for _, step := range scenario.steps {
		run, err := store.CatchUpInvoices(ctx, step.customer, scenario.key, step.through)
		if err != nil {
			t.Fatalf("catch-up customer=%s key=%s error=%v", step.customer, scenario.key, err)
		}
		if len(run.Invoices) != 1 || run.Invoices[0].Number != step.wantNumber || run.ThroughMonth != step.through {
			t.Fatalf("catch-up step=%+v response=%+v; want one invoice", step, run)
		}
	}
	snapshot, err := store.Credit(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.BalanceMicrocents != scenario.wantBalance {
		t.Fatalf("catch-up credit=%s; want %s", snapshot.BalanceMicrocents, scenario.wantBalance)
	}
	var operations int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM api_idempotency_operations").Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if operations != scenario.wantOperations {
		t.Fatalf("scoped operations=%d; want %d", operations, scenario.wantOperations)
	}
}

// testCatchupClosingConcurrency allows either order of a single-month close and
// a missing-range command. Recovery always succeeds and publications remain unique.
func testCatchupClosingConcurrency(t *testing.T) {
	scenario := struct {
		first, through, key          string
		wantMonths                   []string
		wantCloseErrors              []error
		wantInvoices, wantOperations int
	}{first: "2026-10", through: "2026-12", key: "recover", wantMonths: []string{"2026-11", "2026-12"}, wantCloseErrors: []error{nil, billing.ErrConflict}, wantInvoices: 3, wantOperations: 1}
	pool := billingDatabase(t)
	ctx := context.Background()
	store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return parseBillingTime(t, "2027-01-01T00:00:00Z") }})
	if _, err := store.CloseMonth(ctx, "acme", scenario.first); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var group sync.WaitGroup
	var run billing.InvoiceRun
	var batchError, closeError error
	group.Add(2)
	go func() {
		defer group.Done()
		<-start
		run, batchError = store.CatchUpInvoices(ctx, "acme", scenario.key, scenario.through)
	}()
	go func() { defer group.Done(); <-start; _, closeError = store.CloseMonth(ctx, "acme", scenario.through) }()
	close(start)
	group.Wait()
	if batchError != nil {
		t.Fatalf("catch-up concurrent close error=%v; want success", batchError)
	}
	if !errors.Is(closeError, scenario.wantCloseErrors[0]) && !errors.Is(closeError, scenario.wantCloseErrors[1]) {
		t.Fatalf("concurrent close error=%v; want one of %v", closeError, scenario.wantCloseErrors)
	}
	var months []string
	for _, invoice := range run.Invoices {
		months = append(months, invoice.Month)
	}
	if !reflect.DeepEqual(months, scenario.wantMonths) {
		t.Fatalf("concurrent catch-up months=%v; want %v", months, scenario.wantMonths)
	}
	var invoices, operations int
	if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM invoices),(SELECT count(*) FROM api_idempotency_operations)").Scan(&invoices, &operations); err != nil {
		t.Fatal(err)
	}
	if invoices != scenario.wantInvoices || operations != scenario.wantOperations {
		t.Fatalf("concurrent close invoices=%d keys=%d; want %d,%d", invoices, operations, scenario.wantInvoices, scenario.wantOperations)
	}
}

// testCatchupFirstMonthUTC discovers the oldest raw consumption month in both
// session zones, including the exact adjacent boundary, without processing usage.
func testCatchupFirstMonthUTC(t *testing.T) {
	cases := []struct {
		name, zone, start string
		wantMonths        []string
		wantTotals        []int64
		wantRatings       int
		wantPending       int64
	}{
		{name: "UTC last October hour creates two invoices", zone: "UTC", start: "2026-10-31T23:00:00Z", wantMonths: []string{"2026-10", "2026-11"}, wantTotals: []int64{0, 0}, wantRatings: 0, wantPending: 1},
		{name: "Shanghai last October hour remains October", zone: "Asia/Shanghai", start: "2026-10-31T23:00:00Z", wantMonths: []string{"2026-10", "2026-11"}, wantTotals: []int64{0, 0}, wantRatings: 0, wantPending: 1},
		{name: "UTC first November hour starts one invoice", zone: "UTC", start: "2026-11-01T00:00:00Z", wantMonths: []string{"2026-11"}, wantTotals: []int64{0}, wantRatings: 0, wantPending: 1},
		{name: "Shanghai first November hour stays November", zone: "Asia/Shanghai", start: "2026-11-01T00:00:00Z", wantMonths: []string{"2026-11"}, wantTotals: []int64{0}, wantRatings: 0, wantPending: 1},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			original := billingDatabase(t)
			ctx := context.Background()
			config := original.Config()
			config.ConnConfig.RuntimeParams["timezone"] = scenario.zone
			pool, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			var zone string
			if err := pool.QueryRow(ctx, "SHOW timezone").Scan(&zone); err != nil {
				t.Fatal(err)
			}
			if zone != scenario.zone {
				t.Fatalf("catch-up session zone=%s; want %s", zone, scenario.zone)
			}
			store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return parseBillingTime(t, "2026-12-01T00:00:00Z") }})
			insertMeasuredHours(t, store, "acme", []measuredHour{{eventID: "boundary", start: scenario.start, units: 1_000_000}}, "2026-11-02T00:00:00Z")

			result, err := store.CatchUpInvoices(ctx, "acme", "boundary", "2026-11")
			if err != nil {
				t.Fatalf("CatchUpInvoices zone=%s start=%s error=%v; want success", zone, scenario.start, err)
			}
			var months []string
			var totals []int64
			for _, invoice := range result.Invoices {
				months = append(months, invoice.Month)
				totals = append(totals, invoice.TotalCents)
			}
			if !reflect.DeepEqual(months, scenario.wantMonths) || !reflect.DeepEqual(totals, scenario.wantTotals) {
				t.Fatalf("catch-up zone=%s start=%s months=%v totals=%v; want %v,%v", zone, scenario.start, months, totals, scenario.wantMonths, scenario.wantTotals)
			}
			var ratings int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM usage_ratings").Scan(&ratings); err != nil {
				t.Fatal(err)
			}
			snapshot, err := store.Credit(ctx, "acme")
			if err != nil {
				t.Fatal(err)
			}
			if ratings != scenario.wantRatings || snapshot.PendingEvents != scenario.wantPending {
				t.Fatalf("catch-up zone=%s ratings=%d pending=%d; want %d,%d", zone, ratings, snapshot.PendingEvents, scenario.wantRatings, scenario.wantPending)
			}
		})
	}
}
