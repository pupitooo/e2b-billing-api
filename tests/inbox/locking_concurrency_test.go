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
	"github.com/jackc/pgx/v5/pgxpool"
)

// testCreditGrantWorkerOrder forces each account-lock winner in a private schema.
// Literal group, ledger, account and invoice expectations distinguish credit
// available before rating from a later grant that cannot rewrite prior usage.
func testCreditGrantWorkerOrder(t *testing.T) {
	type financialState struct {
		ratings, ledgerEntries    int
		gross, allocated, balance string
		version                   int64
		processed                 bool
	}
	cases := []struct {
		name                                              string
		grantFirst                                        bool
		event                                             usage.Event
		serverTime, receivedAt                            string
		grant                                             billing.CreditGrant
		wantGrantError, wantWorkerError, wantInvoiceError error
		wantWorked                                        bool
		wantState                                         financialState
		wantInvoiceCents                                  int64
	}{
		{name: "grant commits before worker allocates credit", grantFirst: true,
			event:      usage.Event{Source: "credit-lock-order", EventID: "one", SchemaVersion: 1, CustomerID: "acme", SandboxID: "sandbox", Metric: "cpu_seconds", PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z"), Units: 750_000},
			serverTime: "2026-11-01T00:00:00Z", receivedAt: "2026-10-10T13:00:00Z",
			grant:          billing.CreditGrant{IdempotencyKey: "concurrent-grant", AmountCents: 2},
			wantGrantError: nil, wantWorkerError: nil, wantInvoiceError: nil, wantWorked: true,
			wantState: financialState{ratings: 1, ledgerEntries: 2, gross: "3000000", allocated: "2000000", balance: "0", version: 2, processed: true}, wantInvoiceCents: 1},
		{name: "worker commits before a grant and retains its allocation", grantFirst: false,
			event:      usage.Event{Source: "credit-lock-order", EventID: "one", SchemaVersion: 1, CustomerID: "acme", SandboxID: "sandbox", Metric: "cpu_seconds", PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z"), Units: 750_000},
			serverTime: "2026-11-01T00:00:00Z", receivedAt: "2026-10-10T13:00:00Z",
			grant:          billing.CreditGrant{IdempotencyKey: "concurrent-grant", AmountCents: 2},
			wantGrantError: nil, wantWorkerError: nil, wantInvoiceError: nil, wantWorked: true,
			wantState: financialState{ratings: 1, ledgerEntries: 1, gross: "3000000", allocated: "0", balance: "2000000", version: 2, processed: true}, wantInvoiceCents: 3},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return parseBillingTime(t, scenario.serverTime) }})
			event := scenario.event
			if err := store.InsertBatch(ctx, []usage.Event{event}, parseBillingTime(t, scenario.receivedAt)); err != nil {
				t.Fatalf("Insert credit-lock receipt %+v: %v", event, err)
			}
			triggerTable, triggerAction, firstQuery := "usage_inbox", "UPDATE OF processed_at", "UPDATE usage_inbox SET processed_at"
			if scenario.grantFirst {
				triggerTable, triggerAction, firstQuery = "credit_entries", "INSERT", "INSERT INTO credit_entries"
			}
			if _, err := pool.Exec(ctx, `CREATE FUNCTION pause_lock_order() RETURNS trigger LANGUAGE plpgsql AS $f$ BEGIN PERFORM pg_advisory_xact_lock(987004); RETURN NEW; END; $f$; CREATE TRIGGER pause_write BEFORE `+triggerAction+` ON `+triggerTable+` FOR EACH ROW EXECUTE FUNCTION pause_lock_order()`); err != nil {
				t.Fatalf("Prepare grantFirst=%t blocker: %v", scenario.grantFirst, err)
			}
			guard, err := pool.Begin(ctx)
			if err != nil {
				t.Fatalf("Begin grant-order guard: %v", err)
			}
			defer guard.Rollback(context.Background())
			if _, err := guard.Exec(ctx, "SELECT pg_advisory_xact_lock(987004)"); err != nil {
				t.Fatalf("Hold grant-order guard: %v", err)
			}
			type workerResult struct {
				worked bool
				err    error
			}
			grantDone := make(chan error, 1)
			workerDone := make(chan workerResult, 1)
			runGrant := func() { grantDone <- store.GrantCredit(ctx, event.CustomerID, scenario.grant) }
			runWorker := func() { worked, err := store.ProcessBatch(ctx); workerDone <- workerResult{worked: worked, err: err} }

			if scenario.grantFirst {
				go runGrant()
			} else {
				go runWorker()
			}
			waitForDocumentedLock(t, ctx, pool, firstQuery)
			if scenario.grantFirst {
				go runWorker()
			} else {
				go runGrant()
			}
			waitForDocumentedLock(t, ctx, pool, "SELECT credit_balance_microcents::text FROM customer_billing_state")
			if err := guard.Commit(ctx); err != nil {
				t.Fatalf("Release grant-order guard: %v", err)
			}

			grantError, worker := <-grantDone, <-workerDone
			if !errors.Is(grantError, scenario.wantGrantError) {
				t.Fatalf("GrantCredit(%+v) grantFirst=%t error=%v; want %v", scenario.grant, scenario.grantFirst, grantError, scenario.wantGrantError)
			}
			if !errors.Is(worker.err, scenario.wantWorkerError) {
				t.Fatalf("ProcessBatch(%+v) grantFirst=%t error=%v; want %v", event, scenario.grantFirst, worker.err, scenario.wantWorkerError)
			}
			if worker.worked != scenario.wantWorked {
				t.Errorf("ProcessBatch(%+v) worked=%t; want %t", event, worker.worked, scenario.wantWorked)
			}

			var state financialState
			err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM usage_ratings), (SELECT count(*) FROM credit_entries), (SELECT exact_charge_microcents::text FROM rated_usage_groups), (SELECT allocated_credit_microcents::text FROM rated_usage_groups), credit_balance_microcents::text, state_version, (SELECT processed_at IS NOT NULL FROM usage_inbox) FROM customer_billing_state WHERE customer_id=$1`, event.CustomerID).Scan(&state.ratings, &state.ledgerEntries, &state.gross, &state.allocated, &state.balance, &state.version, &state.processed)
			if err != nil {
				t.Fatalf("Read grantFirst=%t financial state: %v", scenario.grantFirst, err)
			}
			if state != scenario.wantState {
				t.Errorf("GrantCredit versus ProcessBatch grantFirst=%t state=%+v; want %+v", scenario.grantFirst, state, scenario.wantState)
			}

			invoice, err := store.CloseMonth(ctx, event.CustomerID, "2026-10")
			if !errors.Is(err, scenario.wantInvoiceError) {
				t.Fatalf("CloseMonth after grantFirst=%t error=%v; want %v", scenario.grantFirst, err, scenario.wantInvoiceError)
			}
			if invoice.TotalCents != scenario.wantInvoiceCents {
				t.Errorf("CloseMonth after grantFirst=%t total=%d; want %d", scenario.grantFirst, invoice.TotalCents, scenario.wantInvoiceCents)
			}
		})
	}
}

// testPriceRatingLockOrder forces both catalog-lock winners. A worker sees a
// price committed before rating; a price inserted after committed rating must
// reject changing that history, without storing a price or successful request.
func testPriceRatingLockOrder(t *testing.T) {
	type financialState struct {
		prices, operations, ratings int
		rate                        int64
		gross, balance              string
		version                     int64
		processed                   bool
	}
	cases := []struct {
		name                            string
		priceFirst                      bool
		event                           usage.Event
		serverTime, receivedAt          string
		input                           billing.PriceInput
		wantPriceError, wantWorkerError error
		wantWorked                      bool
		wantState                       financialState
	}{
		{name: "price commits before worker obtains shared catalog lock", priceFirst: true,
			event:      usage.Event{Source: "catalog-lock-order", EventID: "one", SchemaVersion: 1, CustomerID: "cyberdyne", SandboxID: "sandbox", Metric: "cpu_seconds", PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z"), Units: 1_000_000},
			serverTime: "2026-10-10T12:00:00Z", receivedAt: "2026-10-10T13:00:00Z",
			input:          billing.PriceInput{Metric: "cpu_seconds", PricePerMillionCents: 7, EffectiveFrom: parseBillingTime(t, "2026-10-10T00:00:00Z")},
			wantPriceError: nil, wantWorkerError: nil, wantWorked: true,
			wantState: financialState{prices: 4, operations: 1, ratings: 1, rate: 7, gross: "7000000", balance: "0", version: 1, processed: true}},
		{name: "worker commits before price and protects rated history", priceFirst: false,
			event:      usage.Event{Source: "catalog-lock-order", EventID: "one", SchemaVersion: 1, CustomerID: "cyberdyne", SandboxID: "sandbox", Metric: "cpu_seconds", PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z"), Units: 1_000_000},
			serverTime: "2026-10-10T12:00:00Z", receivedAt: "2026-10-10T13:00:00Z",
			input:          billing.PriceInput{Metric: "cpu_seconds", PricePerMillionCents: 7, EffectiveFrom: parseBillingTime(t, "2026-10-10T00:00:00Z")},
			wantPriceError: billing.ErrConflict, wantWorkerError: nil, wantWorked: true,
			wantState: financialState{prices: 3, operations: 0, ratings: 1, rate: 5, gross: "5000000", balance: "0", version: 1, processed: true}},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return parseBillingTime(t, scenario.serverTime) }})
			event := scenario.event
			if err := store.InsertBatch(ctx, []usage.Event{event}, parseBillingTime(t, scenario.receivedAt)); err != nil {
				t.Fatalf("Insert catalog-lock receipt %+v: %v", event, err)
			}
			triggerTable, triggerAction, firstQuery, secondQuery := "usage_inbox", "UPDATE OF processed_at", "UPDATE usage_inbox SET processed_at", "SELECT pg_advisory_xact_lock($1, $2)"
			if scenario.priceFirst {
				triggerTable, triggerAction, firstQuery, secondQuery = "price_versions", "INSERT", "INSERT INTO price_versions", "SELECT pg_advisory_xact_lock_shared"
			}
			if _, err := pool.Exec(ctx, `CREATE FUNCTION pause_catalog_order() RETURNS trigger LANGUAGE plpgsql AS $f$ BEGIN PERFORM pg_advisory_xact_lock(987005); RETURN NEW; END; $f$; CREATE TRIGGER pause_write BEFORE `+triggerAction+` ON `+triggerTable+` FOR EACH ROW EXECUTE FUNCTION pause_catalog_order()`); err != nil {
				t.Fatalf("Prepare priceFirst=%t blocker: %v", scenario.priceFirst, err)
			}
			guard, err := pool.Begin(ctx)
			if err != nil {
				t.Fatalf("Begin catalog-order guard: %v", err)
			}
			defer guard.Rollback(context.Background())
			if _, err := guard.Exec(ctx, "SELECT pg_advisory_xact_lock(987005)"); err != nil {
				t.Fatalf("Hold catalog-order guard: %v", err)
			}
			type workerResult struct {
				worked bool
				err    error
			}
			priceDone := make(chan error, 1)
			workerDone := make(chan workerResult, 1)
			runPrice := func() { _, err := store.CreatePrice(ctx, "concurrent-price", scenario.input); priceDone <- err }
			runWorker := func() { worked, err := store.ProcessBatch(ctx); workerDone <- workerResult{worked: worked, err: err} }

			if scenario.priceFirst {
				go runPrice()
			} else {
				go runWorker()
			}
			waitForDocumentedLock(t, ctx, pool, firstQuery)
			if scenario.priceFirst {
				go runWorker()
			} else {
				go runPrice()
			}
			waitForDocumentedLock(t, ctx, pool, secondQuery)
			if err := guard.Commit(ctx); err != nil {
				t.Fatalf("Release catalog-order guard: %v", err)
			}

			priceError, worker := <-priceDone, <-workerDone
			if !errors.Is(priceError, scenario.wantPriceError) {
				t.Fatalf("CreatePrice(%+v) priceFirst=%t error=%v; want %v", scenario.input, scenario.priceFirst, priceError, scenario.wantPriceError)
			}
			if !errors.Is(worker.err, scenario.wantWorkerError) {
				t.Fatalf("ProcessBatch(%+v) priceFirst=%t error=%v; want %v", event, scenario.priceFirst, worker.err, scenario.wantWorkerError)
			}
			if worker.worked != scenario.wantWorked {
				t.Errorf("ProcessBatch(%+v) worked=%t; want %t", event, worker.worked, scenario.wantWorked)
			}

			var state financialState
			err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM price_versions), (SELECT count(*) FROM api_idempotency_operations), (SELECT count(*) FROM usage_ratings), (SELECT p.price_per_million_cents FROM rated_usage_groups g JOIN price_versions p USING(price_version_id)), (SELECT exact_charge_microcents::text FROM rated_usage_groups), credit_balance_microcents::text, state_version, (SELECT processed_at IS NOT NULL FROM usage_inbox) FROM customer_billing_state WHERE customer_id=$1`, event.CustomerID).Scan(&state.prices, &state.operations, &state.ratings, &state.rate, &state.gross, &state.balance, &state.version, &state.processed)
			if err != nil {
				t.Fatalf("Read priceFirst=%t financial state: %v", scenario.priceFirst, err)
			}
			if state != scenario.wantState {
				t.Errorf("CreatePrice versus ProcessBatch priceFirst=%t state=%+v; want %+v", scenario.priceFirst, state, scenario.wantState)
			}
		})
	}
}

// waitForDocumentedLock observes the named statement actually waiting in the
// scenario's private pool. It avoids sleeps deciding which transaction wins.
func waitForDocumentedLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, queryFragment string) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE application_name=current_setting('application_name') AND wait_event_type='Lock' AND position($1 in query)>0)`, queryFragment).Scan(&blocked)
		if err != nil {
			t.Fatalf("Observe lock wait for %q: %v", queryFragment, err)
		}
		if blocked {
			return
		}

		select {
		case <-ctx.Done():
			t.Fatalf("Statement %q did not wait before deadline: %v", queryFragment, ctx.Err())
		case <-ticker.C:
		}
	}
}
