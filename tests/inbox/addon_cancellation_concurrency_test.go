//go:build integration

package inbox_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/postgres"
)

// testCancellationConcurrency starts simultaneous commands in a private schema.
// Same-key retries succeed twice with one effect; different keys cannot terminate
// the same purchase twice. Every outcome preserves the usage credit balance.
func testCancellationConcurrency(t *testing.T) {
	cases := []struct {
		name                                                            string
		keys                                                            []string
		wantSuccesses, wantConflicts, wantCancellations, wantOperations int
		wantVersion                                                     int64
		wantCredit                                                      string
	}{
		{name: "identical scoped key", keys: []string{"cancel", "cancel"}, wantSuccesses: 2, wantCancellations: 1, wantOperations: 2, wantVersion: 2, wantCredit: "0"},
		{name: "different keys for one termination", keys: []string{"first", "second"}, wantSuccesses: 1, wantConflicts: 1, wantCancellations: 1, wantOperations: 2, wantVersion: 2, wantCredit: "0"},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			store := newPostgresStore(t, pool, postgres.Config{})
			purchase, err := store.PurchaseAddon(ctx, "acme", "purchase", billing.AddonPurchase{AddonName: "concurrency_pack", PurchasedAt: parseBillingTime(t, "2026-10-01T00:00:00Z")})
			if err != nil {
				t.Fatal(err)
			}
			input := billing.AddonCancellation{SubscriptionID: purchase.SubscriptionID, CancelledAt: parseBillingTime(t, "2026-10-31T00:00:00Z")}
			start := make(chan struct{})
			results := make(chan error, len(scenario.keys))
			for _, key := range scenario.keys {
				go func() { <-start; _, err := store.CancelAddon(ctx, "acme", key, input); results <- err }()
			}

			close(start)
			successes, conflicts := 0, 0
			for range scenario.keys {
				err := <-results
				if err == nil {
					successes++
				} else if errors.Is(err, billing.ErrConflict) {
					conflicts++
				} else {
					t.Fatalf("concurrent cancellation keys=%v error=%v", scenario.keys, err)
				}
			}

			if successes != scenario.wantSuccesses || conflicts != scenario.wantConflicts {
				t.Fatalf("cancellation keys=%v successes=%d conflicts=%d; want %d,%d", scenario.keys, successes, conflicts, scenario.wantSuccesses, scenario.wantConflicts)
			}
			var count, operations int
			var version int64
			var credit string
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM addon_cancellations),(SELECT count(*) FROM api_idempotency_operations),state_version,credit_balance_microcents::text FROM customer_billing_state WHERE customer_id='acme'`).Scan(&count, &operations, &version, &credit); err != nil {
				t.Fatal(err)
			}
			if count != scenario.wantCancellations || operations != scenario.wantOperations || version != scenario.wantVersion || credit != scenario.wantCredit {
				t.Fatalf("concurrent cancellation rows=%d operations=%d version=%d credit=%s; want %+v", count, operations, version, credit, scenario)
			}
		})
	}
}

// testCancellationClosingOrder forces both account-lock winners with database
// gates. Cancelling before closing retains the full last fee; closing first
// rejects the historical cancellation and leaves all future fees active.
func testCancellationClosingOrder(t *testing.T) {
	cases := []struct {
		name, blockingTable                                  string
		cancelFirst                                          bool
		wantCancelStatus, wantCloseStatus, wantCancellations int
		wantVersion, wantTotal                               int64
	}{
		{name: "cancellation wins and last month still costs full price", blockingTable: "addon_cancellations", cancelFirst: true, wantCancelStatus: 200, wantCloseStatus: 200, wantCancellations: 1, wantVersion: 3, wantTotal: 2000},
		{name: "closing wins and historical cancellation is rejected", blockingTable: "invoices", cancelFirst: false, wantCancelStatus: 409, wantCloseStatus: 200, wantCancellations: 0, wantVersion: 2, wantTotal: 2000},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return parseBillingTime(t, "2026-11-01T00:00:00Z") }})
			if _, err := store.PurchaseAddon(ctx, "acme", "purchase", billing.AddonPurchase{AddonName: "concurrency_pack", PurchasedAt: parseBillingTime(t, "2026-10-01T00:00:00Z")}); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `CREATE FUNCTION pause_cancellation_write() RETURNS trigger LANGUAGE plpgsql AS $f$ BEGIN PERFORM pg_advisory_xact_lock(987003); RETURN NEW; END; $f$;
   CREATE TRIGGER pause_write BEFORE INSERT ON `+scenario.blockingTable+` FOR EACH ROW EXECUTE FUNCTION pause_cancellation_write()`); err != nil {
				t.Fatal(err)
			}
			guard, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer guard.Rollback(context.Background())
			if _, err := guard.Exec(ctx, "SELECT pg_advisory_xact_lock(987003)"); err != nil {
				t.Fatal(err)
			}
			handler := newBillingHandler(t, store, 5*time.Second, 8)
			cancellation := make(chan *httptest.ResponseRecorder, 1)
			closing := make(chan *httptest.ResponseRecorder, 1)
			runCancellation := func() {
				cancellation <- financialRequest(t, handler, "POST", "/customers/acme/addons/cancel", `{"idempotency_key":"cancel","subscription_id":"subscription/acme/concurrency_pack","cancelled_at":"2026-10-31"}`)
			}
			runClosing := func() {
				closing <- financialRequest(t, handler, "POST", "/customers/acme/invoices", `{"month":"2026-10"}`)
			}

			if scenario.cancelFirst {
				go runCancellation()
			} else {
				go runClosing()
			}
			waitForClosingLock(t, ctx, pool, "advisory")
			if scenario.cancelFirst {
				go runClosing()
			} else {
				go runCancellation()
			}
			waitForClosingLock(t, ctx, pool, "account")
			if err := guard.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			cancelled, closed := <-cancellation, <-closing

			if cancelled.Code != scenario.wantCancelStatus || closed.Code != scenario.wantCloseStatus {
				t.Fatalf("cancellation-first=%t cancellation=%d close=%d; want %d,%d", scenario.cancelFirst, cancelled.Code, closed.Code, scenario.wantCancelStatus, scenario.wantCloseStatus)
			}
			var count int
			var version, total int64
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM addon_cancellations),state_version,(SELECT total_cents FROM invoices WHERE customer_id='acme') FROM customer_billing_state WHERE customer_id='acme'`).Scan(&count, &version, &total); err != nil {
				t.Fatal(err)
			}
			if count != scenario.wantCancellations || version != scenario.wantVersion || total != scenario.wantTotal {
				t.Fatalf("cancellation/close rows=%d version=%d total=%d; want %+v", count, version, total, scenario)
			}
		})
	}
}
