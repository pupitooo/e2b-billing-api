//go:build integration

package inbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/httpapi"
	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/usage"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// processUsageSteps runs the worker operation with explicit expected work results.
// Financial inputs and expected invoices stay in the calling scenario.
func processUsageSteps(t *testing.T, store *billing.Store, wantWork []bool) {
	t.Helper()
	for step, want := range wantWork {
		worked, err := store.ProcessBatch(context.Background())
		if err != nil {
			t.Fatalf("ProcessBatch step %d: %v; want nil", step+1, err)
		}

		if worked != want {
			t.Fatalf("ProcessBatch step %d worked=%t; want %t", step+1, worked, want)
		}
	}
}

// testInvoiceAPIPending verifies issuance, identical retries and reads through
// HTTP while pending usage, including catalog failures, stays unprocessed.
func testInvoiceAPIPending(t *testing.T) {
	cases := []struct {
		name                                  string
		metric                                string
		processingError                       *string
		wantStatus                            int
		wantInvoice                           invoiceExpectation
		wantProcessed                         bool
		wantRatings, wantInvoices, wantClosed int
		wantNextNumber, wantVersion           int64
	}{
		{name: "valid pending usage", metric: "cpu_seconds", wantStatus: 200,
			wantInvoice:   invoiceExpectation{month: "2026-10", number: "ACME-0001", totalCents: 0, lineCents: []int64{0}, grossTicks: "0", creditTicks: "0", remainingCredit: "0"},
			wantProcessed: false, wantRatings: 0, wantInvoices: 1, wantClosed: 1, wantNextNumber: 2, wantVersion: 1},
		{name: "pending usage with no valid price", metric: "unpriced", wantStatus: 200,
			wantInvoice:   invoiceExpectation{month: "2026-10", number: "ACME-0001", totalCents: 0, lineCents: []int64{0}, grossTicks: "0", creditTicks: "0", remainingCredit: "0"},
			wantProcessed: false, wantRatings: 0, wantInvoices: 1, wantClosed: 1, wantNextNumber: 2, wantVersion: 1},
		{name: "quarantined usage", metric: "cpu_seconds", processingError: stringPointer("investigated failure"), wantStatus: 200,
			wantInvoice:   invoiceExpectation{month: "2026-10", number: "ACME-0001", totalCents: 0, lineCents: []int64{0}, grossTicks: "0", creditTicks: "0", remainingCredit: "0"},
			wantProcessed: false, wantRatings: 0, wantInvoices: 1, wantClosed: 1, wantNextNumber: 2, wantVersion: 1},
	}
	steps := []struct{ method, path, body string }{
		{method: "POST", path: "/customers/acme/invoices", body: `{"month":"2026-10"}`},
		{method: "POST", path: "/customers/acme/invoices", body: `{"month":"2026-10"}`},
		{method: "GET", path: "/customers/acme/invoices/2026-10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			store := billing.NewStoreWithClock(pool, func() time.Time { return parseBillingTime(t, "2026-11-01T00:00:00Z") })
			transport := inbox.NewPostgres(pool, time.Second)
			event := usage.Event{Source: "pending-http", EventID: "one", SchemaVersion: 1, CustomerID: "acme", SandboxID: "sandbox", Metric: tc.metric, PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z"), Units: 1_000_000}
			if err := transport.InsertBatch(ctx, []usage.Event{event}, event.PeriodEnd); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "UPDATE usage_inbox SET processing_error=$1", tc.processingError); err != nil {
				t.Fatal(err)
			}
			handler := httpapi.NewHandler(transport, 5*time.Second, 8, store)
			var original billing.Invoice

			for step, request := range steps {
				response := financialRequest(t, handler, request.method, request.path, request.body)
				if response.Code != tc.wantStatus {
					t.Fatalf("%s %s status=%d body=%s; want %d", request.method, request.path, response.Code, response.Body, tc.wantStatus)
				}
				var invoice billing.Invoice
				if err := json.Unmarshal(response.Body.Bytes(), &invoice); err != nil {
					t.Fatal(err)
				}
				assertInvoiceExpectation(t, store, invoice, tc.wantInvoice)
				if step == 0 {
					original = invoice
				} else if !reflect.DeepEqual(invoice, original) {
					t.Errorf("HTTP step %d invoice=%+v; want original %+v", step+1, invoice, original)
				}
			}

			var processed bool
			var processingError *string
			var ratings, invoices, closed int
			var next, version int64
			if err := pool.QueryRow(ctx, `SELECT processed_at IS NOT NULL,processing_error,
                (SELECT count(*) FROM usage_ratings),(SELECT count(*) FROM invoices),
                (SELECT count(*) FROM closed_billing_months),
                (SELECT next_invoice_number FROM customer_billing_state WHERE customer_id='acme'),
                (SELECT state_version FROM customer_billing_state WHERE customer_id='acme') FROM usage_inbox`).Scan(&processed, &processingError, &ratings, &invoices, &closed, &next, &version); err != nil {
				t.Fatal(err)
			}
			if processed != tc.wantProcessed || !reflect.DeepEqual(processingError, tc.processingError) || ratings != tc.wantRatings || invoices != tc.wantInvoices || closed != tc.wantClosed || next != tc.wantNextNumber || version != tc.wantVersion {
				t.Errorf("HTTP closing state processed=%t error=%v ratings=%d invoices=%d closed=%d next=%d version=%d; want %+v", processed, processingError, ratings, invoices, closed, next, version, tc)
			}
		})
	}
}

// testProcessedClosing checks zero, partial, and complete worker progress at the
// cutoff. Pending usage stays untouched, keeps its historical price and usage
// month, and consumes credit exactly once when the worker processes it later.
func testProcessedClosing(t *testing.T) {
	cases := []struct {
		name                  string
		units                 int64
		initialCreditTicks    string
		beforeClosingWork     []bool
		afterClosingWork      []bool
		wantPendingAtClosing  int64
		wantOctober           invoiceExpectation
		wantNovember          invoiceExpectation
		wantOriginalMonth     string
		wantFinalRatings      int
		wantFinalMonthlyGross string
		wantNextNumber        int64
	}{
		{
			name: "all accepted usage is still pending", units: 100_000_000, initialCreditTicks: "600000000",
			beforeClosingWork: []bool{}, afterClosingWork: []bool{true, true, false}, wantPendingAtClosing: 2,
			wantOctober:       invoiceExpectation{month: "2026-10", number: "ACME-0001", totalCents: 0, lineCents: []int64{0}, grossTicks: "0", creditTicks: "0", remainingCredit: "600000000"},
			wantNovember:      invoiceExpectation{month: "2026-11", number: "ACME-0002", totalCents: 200, lineCents: []int64{800, -600}, grossTicks: "800000000", creditTicks: "600000000", remainingCredit: "0"},
			wantOriginalMonth: "2026-10", wantFinalRatings: 2, wantFinalMonthlyGross: "800000000", wantNextNumber: 3,
		},
		{
			name: "one processed usage and one pending usage share the original price", units: 100_000_000, initialCreditTicks: "600000000",
			beforeClosingWork: []bool{true}, afterClosingWork: []bool{true, false}, wantPendingAtClosing: 1,
			wantOctober:       invoiceExpectation{month: "2026-10", number: "ACME-0001", totalCents: 0, lineCents: []int64{400, -400}, grossTicks: "400000000", creditTicks: "400000000", remainingCredit: "200000000"},
			wantNovember:      invoiceExpectation{month: "2026-11", number: "ACME-0002", totalCents: 200, lineCents: []int64{400, -200}, grossTicks: "400000000", creditTicks: "200000000", remainingCredit: "0"},
			wantOriginalMonth: "2026-10", wantFinalRatings: 2, wantFinalMonthlyGross: "800000000", wantNextNumber: 3,
		},
		{
			name: "all usage was processed before closing", units: 100_000_000, initialCreditTicks: "600000000",
			beforeClosingWork: []bool{true, true}, afterClosingWork: []bool{false}, wantPendingAtClosing: 0,
			wantOctober:       invoiceExpectation{month: "2026-10", number: "ACME-0001", totalCents: 200, lineCents: []int64{800, -600}, grossTicks: "800000000", creditTicks: "600000000", remainingCredit: "0"},
			wantNovember:      invoiceExpectation{month: "2026-11", number: "ACME-0002", totalCents: 0, lineCents: []int64{0}, grossTicks: "0", creditTicks: "0", remainingCredit: "0"},
			wantOriginalMonth: "2026-10", wantFinalRatings: 2, wantFinalMonthlyGross: "800000000", wantNextNumber: 3,
		},
		{
			name: "two half-cent charges split between invoices round separately", units: 125_000, initialCreditTicks: "0",
			beforeClosingWork: []bool{true}, afterClosingWork: []bool{true, false}, wantPendingAtClosing: 1,
			wantOctober:       invoiceExpectation{month: "2026-10", number: "ACME-0001", totalCents: 1, lineCents: []int64{1, 0}, grossTicks: "500000", creditTicks: "0", remainingCredit: "0"},
			wantNovember:      invoiceExpectation{month: "2026-11", number: "ACME-0002", totalCents: 1, lineCents: []int64{1, 0}, grossTicks: "500000", creditTicks: "0", remainingCredit: "0"},
			wantOriginalMonth: "2026-10", wantFinalRatings: 2, wantFinalMonthlyGross: "1000000", wantNextNumber: 3,
		},
		{
			name: "two half-cent charges processed before closing round together", units: 125_000, initialCreditTicks: "0",
			beforeClosingWork: []bool{true, true}, afterClosingWork: []bool{false}, wantPendingAtClosing: 0,
			wantOctober:       invoiceExpectation{month: "2026-10", number: "ACME-0001", totalCents: 1, lineCents: []int64{1, 0}, grossTicks: "1000000", creditTicks: "0", remainingCredit: "0"},
			wantNovember:      invoiceExpectation{month: "2026-11", number: "ACME-0002", totalCents: 0, lineCents: []int64{0}, grossTicks: "0", creditTicks: "0", remainingCredit: "0"},
			wantOriginalMonth: "2026-10", wantFinalRatings: 2, wantFinalMonthlyGross: "1000000", wantNextNumber: 3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			if _, err := pool.Exec(ctx, "UPDATE customer_billing_state SET credit_balance_ticks=$1::numeric WHERE customer_id='acme'", tc.initialCreditTicks); err != nil {
				t.Fatal(err)
			}
			store := billing.NewStoreWithClock(pool, func() time.Time { return parseBillingTime(t, "2026-12-01T00:00:00Z") })
			transport := inbox.NewPostgres(pool, time.Second)
			hours := []measuredHour{
				{eventID: "one", start: "2026-10-10T12:00:00Z", units: tc.units},
				{eventID: "two", start: "2026-10-10T13:00:00Z", units: tc.units},
			}
			insertMeasuredHours(t, transport, "acme", hours, "2026-10-11T00:00:00Z")
			processUsageSteps(t, store, tc.beforeClosingWork)

			october, err := store.CloseMonth(ctx, "acme", "2026-10")
			if err != nil {
				t.Fatalf("CloseMonth after work steps %v: %v", tc.beforeClosingWork, err)
			}
			assertInvoiceExpectation(t, store, october, tc.wantOctober)

			credit, err := store.Credit(ctx, "acme")
			if err != nil {
				t.Fatal(err)
			}
			if credit.PendingEvents != tc.wantPendingAtClosing {
				t.Errorf("Pending usage after CloseMonth=%d; want %d", credit.PendingEvents, tc.wantPendingAtClosing)
			}

			processUsageSteps(t, store, tc.afterClosingWork)
			retry, err := store.CloseMonth(ctx, "acme", "2026-10")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(retry, october) {
				t.Errorf("Retry after worker progress=%+v; want original %+v", retry, october)
			}

			november, err := store.CloseMonth(ctx, "acme", "2026-11")
			if err != nil {
				t.Fatal(err)
			}
			assertInvoiceExpectation(t, store, november, tc.wantNovember)

			var ratings int
			var originalMonth, gross string
			var nextNumber int64
			if err := pool.QueryRow(ctx, `SELECT to_char(usage_month,'YYYY-MM'),gross_charge_ticks::text,
                (SELECT count(*) FROM usage_ratings),
                (SELECT next_invoice_number FROM customer_billing_state WHERE customer_id='acme')
                FROM monthly_usage WHERE customer_id='acme'`).Scan(&originalMonth, &gross, &ratings, &nextNumber); err != nil {
				t.Fatal(err)
			}
			if originalMonth != tc.wantOriginalMonth || gross != tc.wantFinalMonthlyGross || ratings != tc.wantFinalRatings || nextNumber != tc.wantNextNumber {
				t.Errorf("Final month=%s gross=%s ratings=%d next=%d; want %s %s %d %d", originalMonth, gross, ratings, nextNumber, tc.wantOriginalMonth, tc.wantFinalMonthlyGross, tc.wantFinalRatings, tc.wantNextNumber)
			}
		})
	}
}

// testClosingWorkerOrder deliberately pauses the lock winner in PostgreSQL.
// Both orders must include usage exactly once, preserve the historical month,
// and place it on the invoice determined by the customer account lock order.
func testClosingWorkerOrder(t *testing.T) {
	cases := []struct {
		name              string
		workerFirst       bool
		blockingSQL       string
		wantOctoberTotal  int64
		wantNovemberTotal int64
		wantBillingMonth  string
		wantRatings       int
		wantWorkerResult  bool
	}{
		{
			name: "in-flight worker commits before closing acquires the account", workerFirst: true,
			blockingSQL: `CREATE FUNCTION pause_financial_write() RETURNS trigger LANGUAGE plpgsql AS $f$
                BEGIN PERFORM pg_advisory_xact_lock(987001); RETURN NEW; END; $f$;
                CREATE TRIGGER pause_write BEFORE UPDATE OF processed_at ON usage_inbox
                FOR EACH ROW EXECUTE FUNCTION pause_financial_write()`,
			wantOctoberTotal: 4, wantNovemberTotal: 0, wantBillingMonth: "2026-10", wantRatings: 1, wantWorkerResult: true,
		},
		{
			name: "closing commits before the waiting worker can account usage", workerFirst: false,
			blockingSQL: `CREATE FUNCTION pause_financial_write() RETURNS trigger LANGUAGE plpgsql AS $f$
                BEGIN PERFORM pg_advisory_xact_lock(987001); RETURN NEW; END; $f$;
                CREATE TRIGGER pause_write BEFORE INSERT ON invoices
                FOR EACH ROW EXECUTE FUNCTION pause_financial_write()`,
			wantOctoberTotal: 0, wantNovemberTotal: 4, wantBillingMonth: "2026-11", wantRatings: 1, wantWorkerResult: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			store := billing.NewStoreWithClock(pool, func() time.Time { return parseBillingTime(t, "2026-12-01T00:00:00Z") })
			event := usage.Event{Source: "lock-order", EventID: "one", SchemaVersion: 1, CustomerID: "acme", SandboxID: "sandbox", Metric: "cpu_seconds", PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z"), Units: 1_000_000}
			if err := inbox.NewPostgres(pool, time.Second).InsertBatch(ctx, []usage.Event{event}, event.PeriodEnd); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, tc.blockingSQL); err != nil {
				t.Fatal(err)
			}
			guard, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer guard.Rollback(context.Background())
			if _, err := guard.Exec(ctx, "SELECT pg_advisory_xact_lock(987001)"); err != nil {
				t.Fatal(err)
			}
			type workerResult struct {
				worked bool
				err    error
			}
			type closingResult struct {
				invoice billing.Invoice
				err     error
			}
			workerDone := make(chan workerResult, 1)
			closingDone := make(chan closingResult, 1)
			runWorker := func() { worked, err := store.ProcessBatch(ctx); workerDone <- workerResult{worked: worked, err: err} }
			runClosing := func() {
				invoice, err := store.CloseMonth(ctx, "acme", "2026-10")
				closingDone <- closingResult{invoice: invoice, err: err}
			}

			if tc.workerFirst {
				go runWorker()
			} else {
				go runClosing()
			}
			waitForClosingLock(t, ctx, pool, "advisory")
			if tc.workerFirst {
				go runClosing()
			} else {
				go runWorker()
			}
			waitForClosingLock(t, ctx, pool, "account")
			if err := guard.Commit(ctx); err != nil {
				t.Fatal(err)
			}

			worker := <-workerDone
			if worker.err != nil {
				t.Fatalf("Worker with workerFirst=%t: %v", tc.workerFirst, worker.err)
			}
			if worker.worked != tc.wantWorkerResult {
				t.Errorf("Worker result=%t; want %t", worker.worked, tc.wantWorkerResult)
			}
			closed := <-closingDone
			if closed.err != nil {
				t.Fatalf("Closing with workerFirst=%t: %v", tc.workerFirst, closed.err)
			}
			if closed.invoice.TotalCents != tc.wantOctoberTotal {
				t.Errorf("October total=%d; want %d", closed.invoice.TotalCents, tc.wantOctoberTotal)
			}

			if _, err := pool.Exec(ctx, "DROP FUNCTION pause_financial_write() CASCADE"); err != nil {
				t.Fatal(err)
			}
			november, err := store.CloseMonth(ctx, "acme", "2026-11")
			if err != nil {
				t.Fatal(err)
			}
			if november.TotalCents != tc.wantNovemberTotal {
				t.Errorf("November total=%d; want %d", november.TotalCents, tc.wantNovemberTotal)
			}
			var month string
			var ratings int
			if err := pool.QueryRow(ctx, `SELECT to_char(billing_month,'YYYY-MM'),
                (SELECT count(*) FROM usage_ratings) FROM rated_usage_groups`).Scan(&month, &ratings); err != nil {
				t.Fatal(err)
			}
			if month != tc.wantBillingMonth || ratings != tc.wantRatings {
				t.Errorf("Worker billing month=%s ratings=%d; want %s %d", month, ratings, tc.wantBillingMonth, tc.wantRatings)
			}
		})
	}
}

// waitForClosingLock observes a real blocked database operation in the private
// schema's application sessions; no sleeps determine which operation wins.
func waitForClosingLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind string) {
	t.Helper()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		var blocked bool
		err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
            WHERE application_name=current_setting('application_name') AND wait_event_type='Lock'
            AND (($1='advisory' AND wait_event='advisory') OR
                ($1='account' AND query LIKE 'SELECT credit_balance_ticks::text FROM customer_billing_state%')))`, kind).Scan(&blocked)
		if err != nil {
			t.Fatalf("Observe blocked %s lock: %v", kind, err)
		}
		if blocked {
			return
		}

		select {
		case <-ctx.Done():
			t.Fatalf("No blocked %s operation before deadline: %v", kind, ctx.Err())
		case <-poll.C:
		}
	}
}

// testClosingRollback fails a late publication write after usage was already
// accounted. Closure, snapshot, group freeze and numbering all roll back, while
// prior worker financial effects remain. Retry includes later worker progress
// without consuming another invoice number.
func testClosingRollback(t *testing.T) {
	scenario := struct {
		units                                  int64
		afterFailureUnits                      int64
		initialCreditTicks                     string
		wantFailure                            bool
		wantSQLState                           string
		wantInvoices, wantClosures, wantFrozen int
		wantNextNumber, wantVersion            int64
		wantCredit                             string
		wantRetry                              invoiceExpectation
	}{units: 100_000_000, initialCreditTicks: "600000000", afterFailureUnits: 100_000_000, wantFailure: true, wantSQLState: "P0001",
		wantInvoices: 0, wantClosures: 0, wantFrozen: 0, wantNextNumber: 1, wantVersion: 1, wantCredit: "200000000",
		wantRetry: invoiceExpectation{month: "2026-10", number: "ACME-0001", totalCents: 200, lineCents: []int64{800, -600}, grossTicks: "800000000", creditTicks: "600000000", remainingCredit: "0"}}
	pool := billingDatabase(t)
	ctx := context.Background()
	store := billing.NewStoreWithClock(pool, func() time.Time { return parseBillingTime(t, "2026-11-01T00:00:00Z") })
	if _, err := pool.Exec(ctx, "UPDATE customer_billing_state SET credit_balance_ticks=$1::numeric WHERE customer_id='acme'", scenario.initialCreditTicks); err != nil {
		t.Fatal(err)
	}
	insertMeasuredHours(t, inbox.NewPostgres(pool, time.Second), "acme", []measuredHour{{eventID: "one", start: "2026-10-10T12:00:00Z", units: scenario.units}}, "2026-10-11T00:00:00Z")
	processUsageSteps(t, store, []bool{true})
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_publication() RETURNS trigger LANGUAGE plpgsql AS $f$
        BEGIN RAISE EXCEPTION 'injected freeze failure'; END; $f$;
        CREATE TRIGGER fail_freeze BEFORE INSERT ON invoiced_usage_groups FOR EACH ROW EXECUTE FUNCTION fail_publication()`); err != nil {
		t.Fatal(err)
	}

	_, err := store.CloseMonth(ctx, "acme", "2026-10")
	if (err != nil) != scenario.wantFailure {
		t.Fatalf("Injected publication error=%v; wantError %t", err, scenario.wantFailure)
	}
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) || databaseError.Code != scenario.wantSQLState {
		t.Fatalf("Injected publication error=%v; want SQLSTATE %s", err, scenario.wantSQLState)
	}

	var invoices, closures, frozen int
	var next, version int64
	var credit string
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM invoices),(SELECT count(*) FROM closed_billing_months),
        (SELECT count(*) FROM invoiced_usage_groups),next_invoice_number,state_version,credit_balance_ticks::text
        FROM customer_billing_state WHERE customer_id='acme'`).Scan(&invoices, &closures, &frozen, &next, &version, &credit); err != nil {
		t.Fatal(err)
	}
	if invoices != scenario.wantInvoices || closures != scenario.wantClosures || frozen != scenario.wantFrozen || next != scenario.wantNextNumber || version != scenario.wantVersion || credit != scenario.wantCredit {
		t.Errorf("Failed publication invoices=%d closed=%d frozen=%d next=%d version=%d credit=%s; want %d %d %d %d %d %s", invoices, closures, frozen, next, version, credit, scenario.wantInvoices, scenario.wantClosures, scenario.wantFrozen, scenario.wantNextNumber, scenario.wantVersion, scenario.wantCredit)
	}

	if _, err := pool.Exec(ctx, "DROP FUNCTION fail_publication() CASCADE"); err != nil {
		t.Fatal(err)
	}
	insertMeasuredHours(t, inbox.NewPostgres(pool, time.Second), "acme", []measuredHour{{eventID: "after-failure", start: "2026-10-10T13:00:00Z", units: scenario.afterFailureUnits}}, "2026-10-11T00:00:00Z")
	processUsageSteps(t, store, []bool{true, false})

	retry, err := store.CloseMonth(ctx, "acme", "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	assertInvoiceExpectation(t, store, retry, scenario.wantRetry)
}

// testClosingHistory preserves chronological closing and rejects attempts that
// would strand an earlier processed group, while allowing its adjacent valid order.
func testClosingHistory(t *testing.T) {
	cases := []struct {
		name            string
		processedUsage  bool
		pendingUsage    bool
		processingError *string
		closeFirst      []string
		month           string
		wantError       error
		wantInvoices    int
	}{
		{name: "first invoice cannot skip an older pending usage month", pendingUsage: true, month: "2026-11", wantError: billing.ErrConflict, wantInvoices: 0},
		{name: "first invoice cannot skip an older quarantined usage month", pendingUsage: true, processingError: stringPointer("investigated failure"), month: "2026-11", wantError: billing.ErrConflict, wantInvoices: 0},
		{name: "first invoice can close the oldest pending month", pendingUsage: true, month: "2026-10", wantError: nil, wantInvoices: 1},
		{name: "first empty invoice can start in any completed month", month: "2026-11", wantError: nil, wantInvoices: 1},
		{name: "empty December cannot skip unclosed November", closeFirst: []string{"2026-10"}, month: "2026-12", wantError: billing.ErrConflict, wantInvoices: 1},
		{name: "empty consecutive November is permitted", closeFirst: []string{"2026-10"}, month: "2026-11", wantError: nil, wantInvoices: 2},
		{name: "earlier processed group must be invoiced first", processedUsage: true, month: "2026-11", wantError: billing.ErrConflict, wantInvoices: 0},
		{name: "earlier closed group allows the next month", processedUsage: true, closeFirst: []string{"2026-10"}, month: "2026-11", wantError: nil, wantInvoices: 2},
		{name: "a later closed month prevents backward closing", closeFirst: []string{"2026-11"}, month: "2026-10", wantError: billing.ErrConflict, wantInvoices: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			store := billing.NewStoreWithClock(pool, func() time.Time { return parseBillingTime(t, "2027-01-01T00:00:00Z") })
			if tc.processedUsage || tc.pendingUsage {
				insertMeasuredHours(t, inbox.NewPostgres(pool, time.Second), "acme", []measuredHour{{eventID: "one", start: "2026-10-10T12:00:00Z", units: 1_000_000}}, "2026-10-11T00:00:00Z")
				if tc.processingError != nil {
					if _, err := pool.Exec(context.Background(), "UPDATE usage_inbox SET processing_error=$1", tc.processingError); err != nil {
						t.Fatal(err)
					}
				}
				if tc.processedUsage {
					processUsageSteps(t, store, []bool{true})
				}
			}
			for _, month := range tc.closeFirst {
				if _, err := store.CloseMonth(context.Background(), "acme", month); err != nil {
					t.Fatal(err)
				}
			}

			_, err := store.CloseMonth(context.Background(), "acme", tc.month)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("CloseMonth(%s) after %v: error=%v; want %v", tc.month, tc.closeFirst, err, tc.wantError)
			}

			var invoices int
			if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM invoices").Scan(&invoices); err != nil {
				t.Fatal(err)
			}
			if invoices != tc.wantInvoices {
				t.Errorf("Invoice count=%d; want %d", invoices, tc.wantInvoices)
			}
		})
	}
}

// testClosingSkippedMonths invoices late or pending usage after the latest closed
// month, including periods before the first invoice. Price and spend stay in October;
// the original snapshots and rejected backward closing have no financial effects.
func testClosingSkippedMonths(t *testing.T) {
	cases := []struct {
		acceptBeforeClosing       bool
		name                      string
		closeFirst                []string
		receivedAt                string
		wantBillingMonth          string
		wantInvoice               invoiceExpectation
		wantBackwardError         error
		wantUsageMonth, wantGross string
		wantRatings               int
		wantNextNumber            int64
	}{
		{name: "late October follows the first invoice in November", closeFirst: []string{"2026-11"}, receivedAt: "2026-10-11T00:00:00Z", wantBillingMonth: "2026-12",
			wantInvoice:       invoiceExpectation{month: "2026-12", number: "ACME-0002", totalCents: 4, lineCents: []int64{4, 0}, grossTicks: "4000000", creditTicks: "0", remainingCredit: "0"},
			wantBackwardError: billing.ErrConflict, wantUsageMonth: "2026-10", wantGross: "4000000", wantRatings: 1, wantNextNumber: 3},
		{name: "pending October survives two successful closures", acceptBeforeClosing: true, closeFirst: []string{"2026-10", "2026-11"}, receivedAt: "2026-10-11T00:00:00Z", wantBillingMonth: "2026-12",
			wantInvoice:       invoiceExpectation{month: "2026-12", number: "ACME-0003", totalCents: 4, lineCents: []int64{4, 0}, grossTicks: "4000000", creditTicks: "0", remainingCredit: "0"},
			wantBackwardError: nil, wantUsageMonth: "2026-10", wantGross: "4000000", wantRatings: 1, wantNextNumber: 4},
		{name: "receipt month after latest closure bounds the next invoice", closeFirst: []string{"2026-11", "2026-12"}, receivedAt: "2027-01-10T00:00:00Z", wantBillingMonth: "2027-01",
			wantInvoice:       invoiceExpectation{month: "2027-01", number: "ACME-0003", totalCents: 4, lineCents: []int64{4, 0}, grossTicks: "4000000", creditTicks: "0", remainingCredit: "0"},
			wantBackwardError: billing.ErrConflict, wantUsageMonth: "2026-10", wantGross: "4000000", wantRatings: 1, wantNextNumber: 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			store := billing.NewStoreWithClock(pool, func() time.Time { return parseBillingTime(t, "2027-02-01T00:00:00Z") })
			if tc.acceptBeforeClosing {
				insertMeasuredHours(t, inbox.NewPostgres(pool, time.Second), "acme", []measuredHour{{eventID: "pending", start: "2026-10-10T12:00:00Z", units: 1_000_000}}, tc.receivedAt)
			}
			original := make([]billing.Invoice, 0, len(tc.closeFirst))
			for _, month := range tc.closeFirst {
				invoice, err := store.CloseMonth(ctx, "acme", month)
				if err != nil {
					t.Fatal(err)
				}
				if invoice.TotalCents != 0 || invoice.GrossUsageTicks != "0" {
					t.Fatalf("CloseMonth(%s) before worker=%+v; want zero usage and total", month, invoice)
				}
				original = append(original, invoice)
			}

			if !tc.acceptBeforeClosing {
				insertMeasuredHours(t, inbox.NewPostgres(pool, time.Second), "acme", []measuredHour{{eventID: "pending", start: "2026-10-10T12:00:00Z", units: 1_000_000}}, tc.receivedAt)
			}

			processUsageSteps(t, store, []bool{true, false})
			_, err := store.CloseMonth(ctx, "acme", "2026-10")
			if !errors.Is(err, tc.wantBackwardError) {
				t.Fatalf("Backward CloseMonth error=%v; want %v", err, tc.wantBackwardError)
			}
			invoice, err := store.CloseMonth(ctx, "acme", tc.wantInvoice.month)
			if err != nil {
				t.Fatal(err)
			}
			assertInvoiceExpectation(t, store, invoice, tc.wantInvoice)

			for step, month := range tc.closeFirst {
				retry, err := store.CloseMonth(ctx, "acme", month)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(retry, original[step]) {
					t.Errorf("Retry %s=%+v; want original %+v", month, retry, original[step])
				}
			}
			var usageMonth, billingMonth, spendMonth, gross string
			var ratings int
			var next int64
			if err := pool.QueryRow(ctx, `SELECT to_char(g.usage_month,'YYYY-MM'),to_char(g.billing_month,'YYYY-MM'),
                to_char(m.usage_month,'YYYY-MM'),m.gross_charge_ticks::text,
                (SELECT count(*) FROM usage_ratings),(SELECT next_invoice_number FROM customer_billing_state WHERE customer_id='acme')
                FROM rated_usage_groups g JOIN monthly_usage m USING(customer_id,usage_month)`).Scan(&usageMonth, &billingMonth, &spendMonth, &gross, &ratings, &next); err != nil {
				t.Fatal(err)
			}
			if usageMonth != tc.wantUsageMonth || spendMonth != tc.wantUsageMonth || billingMonth != tc.wantBillingMonth || gross != tc.wantGross || ratings != tc.wantRatings || next != tc.wantNextNumber {
				t.Errorf("Final months=%s/%s/%s gross=%s ratings=%d next=%d; want %+v", usageMonth, billingMonth, spendMonth, gross, ratings, next, tc)
			}
		})
	}
}
