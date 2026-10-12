//go:build integration

package inbox_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"e2b/billing-api/internal/postgres"
	"e2b/billing-api/internal/usage"
	"github.com/jackc/pgx/v5/pgconn"
)

// testMaintenanceLockOrder runs the real worker against controlled maintenance
// in a private schema. Reversed advisory or relation order must expose one
// PostgreSQL deadlock victim; the adjacent forward order completes normally.
// Either possible victim preserves atomic accounting, followed by safe retry.
func testMaintenanceLockOrder(t *testing.T) {
	type financialState struct {
		ratings, groups, monthlyRows, ledgerRows int
		balance, gross, allocated                string
		version                                  int64
		processed                                bool
	}
	type outcome struct {
		state              financialState
		wantWorkerSQLState string
		wantRetryWorked    bool
		wantWorkerWorked   bool
	}
	cases := []struct {
		name, initialSQL, finalSQL, workerWaitSQL string
		wantSQLStates                             []string
		wantOutcomes                              []outcome
	}{
		{name: "account row before catalog avoids the cycle", initialSQL: "SELECT customer_id FROM customer_billing_state WHERE customer_id='acme' FOR UPDATE", finalSQL: "SELECT pg_advisory_xact_lock(65102,2)", workerWaitSQL: "SELECT credit_balance_microcents::text FROM customer_billing_state", wantSQLStates: []string{"", ""},
			wantOutcomes: []outcome{{state: financialState{ratings: 1, groups: 1, monthlyRows: 1, ledgerRows: 1, balance: "0", gross: "4000000", allocated: "3000000", version: 1, processed: true}, wantWorkerSQLState: "", wantRetryWorked: false, wantWorkerWorked: true}}},
		{name: "catalog before account row creates a cycle", initialSQL: "SELECT pg_advisory_xact_lock(65102,2)", finalSQL: "SELECT customer_id FROM customer_billing_state WHERE customer_id='acme' FOR UPDATE", workerWaitSQL: "SELECT pg_advisory_xact_lock_shared", wantSQLStates: []string{"", "40P01"},
			wantOutcomes: []outcome{
				{state: financialState{ratings: 1, groups: 1, monthlyRows: 1, ledgerRows: 1, balance: "0", gross: "4000000", allocated: "3000000", version: 1, processed: true}, wantWorkerSQLState: "", wantRetryWorked: false, wantWorkerWorked: true},
				{state: financialState{ratings: 0, groups: 0, monthlyRows: 0, ledgerRows: 0, balance: "3000000", gross: "0", allocated: "0", version: 0, processed: false}, wantWorkerSQLState: "40P01", wantRetryWorked: true, wantWorkerWorked: false}}},
		{name: "account relation before inbox relation avoids this DDL cycle", initialSQL: "LOCK TABLE customer_billing_state IN ACCESS EXCLUSIVE MODE", finalSQL: "LOCK TABLE usage_inbox IN SHARE ROW EXCLUSIVE MODE", workerWaitSQL: "SELECT credit_balance_microcents::text FROM customer_billing_state", wantSQLStates: []string{"", ""},
			wantOutcomes: []outcome{{state: financialState{ratings: 1, groups: 1, monthlyRows: 1, ledgerRows: 1, balance: "0", gross: "4000000", allocated: "3000000", version: 1, processed: true}, wantWorkerSQLState: "", wantRetryWorked: false, wantWorkerWorked: true}}},
		{name: "inbox relation before account relation creates a DDL cycle", initialSQL: "LOCK TABLE usage_inbox IN SHARE ROW EXCLUSIVE MODE", finalSQL: "LOCK TABLE customer_billing_state IN ACCESS EXCLUSIVE MODE", workerWaitSQL: "UPDATE usage_inbox SET processed_at", wantSQLStates: []string{"", "40P01"},
			wantOutcomes: []outcome{
				{state: financialState{ratings: 1, groups: 1, monthlyRows: 1, ledgerRows: 1, balance: "0", gross: "4000000", allocated: "3000000", version: 1, processed: true}, wantWorkerSQLState: "", wantRetryWorked: false, wantWorkerWorked: true},
				{state: financialState{ratings: 0, groups: 0, monthlyRows: 0, ledgerRows: 0, balance: "3000000", gross: "0", allocated: "0", version: 0, processed: false}, wantWorkerSQLState: "40P01", wantRetryWorked: true, wantWorkerWorked: false}}},
	}
	input := struct {
		event                                 usage.Event
		initialCredit, serverTime, receivedAt string
		wantFinalState                        financialState
	}{
		event:         usage.Event{Source: "maintenance-order", EventID: "one", SchemaVersion: 1, CustomerID: "acme", SandboxID: "sandbox", Metric: "cpu_seconds", PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z"), Units: 1_000_000},
		initialCredit: "3000000", serverTime: "2026-11-01T00:00:00Z", receivedAt: "2026-10-10T13:00:00Z",
		wantFinalState: financialState{ratings: 1, groups: 1, monthlyRows: 1, ledgerRows: 1, balance: "0", gross: "4000000", allocated: "3000000", version: 1, processed: true},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return parseBillingTime(t, input.serverTime) }})
			if _, err := pool.Exec(ctx, "UPDATE customer_billing_state SET credit_balance_microcents=$1::numeric WHERE customer_id=$2", input.initialCredit, input.event.CustomerID); err != nil {
				t.Fatalf("Prepare maintenance credit=%s: %v", input.initialCredit, err)
			}
			if err := store.InsertBatch(ctx, []usage.Event{input.event}, parseBillingTime(t, input.receivedAt)); err != nil {
				t.Fatalf("Insert maintenance input %+v: %v", input.event, err)
			}
			maintenance, err := pool.Begin(ctx)
			if err != nil {
				t.Fatalf("Begin maintenance: %v", err)
			}
			defer maintenance.Rollback(context.Background())
			if _, err := maintenance.Exec(ctx, "SET LOCAL deadlock_timeout = '100ms'"); err != nil {
				t.Fatalf("Set test deadlock detection interval: %v", err)
			}
			if _, err := maintenance.Exec(ctx, scenario.initialSQL); err != nil {
				t.Fatalf("Initial maintenance SQL %q: %v", scenario.initialSQL, err)
			}
			type workerResult struct {
				worked bool
				err    error
			}
			workerDone := make(chan workerResult, 1)

			go func() { worked, err := store.ProcessBatch(ctx); workerDone <- workerResult{worked: worked, err: err} }()
			waitForDocumentedLock(t, ctx, pool, scenario.workerWaitSQL)
			_, maintenanceError := maintenance.Exec(ctx, scenario.finalSQL)
			if maintenanceError == nil {
				if err := maintenance.Commit(ctx); err != nil {
					t.Fatalf("Commit maintenance SQL %q: %v", scenario.finalSQL, err)
				}
			} else {
				if err := maintenance.Rollback(ctx); err != nil {
					t.Fatalf("Rollback maintenance after %v: %v", maintenanceError, err)
				}
			}
			worker := <-workerDone

			sqlState := func(err error) string {
				if err == nil {
					return ""
				}
				var databaseError *pgconn.PgError
				if !errors.As(err, &databaseError) {
					t.Fatalf("Maintenance %q produced non-SQL error: %v", scenario.name, err)
				}
				return databaseError.Code
			}
			workerSQLState := sqlState(worker.err)
			actualSQLStates := []string{sqlState(maintenanceError), workerSQLState}
			sort.Strings(actualSQLStates)
			if !reflect.DeepEqual(actualSQLStates, scenario.wantSQLStates) {
				t.Fatalf("Maintenance %q and worker errors=%v,%v SQLSTATE=%v; want %v", scenario.name, maintenanceError, worker.err, actualSQLStates, scenario.wantSQLStates)
			}

			readState := func() financialState {
				var state financialState
				err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM usage_ratings),(SELECT count(*) FROM rated_usage_groups),(SELECT count(*) FROM monthly_usage),(SELECT count(*) FROM credit_entries),credit_balance_microcents::text,COALESCE((SELECT exact_charge_microcents::text FROM rated_usage_groups),'0'),COALESCE((SELECT allocated_credit_microcents::text FROM rated_usage_groups),'0'),state_version,(SELECT processed_at IS NOT NULL FROM usage_inbox) FROM customer_billing_state WHERE customer_id=$1`, input.event.CustomerID).Scan(&state.ratings, &state.groups, &state.monthlyRows, &state.ledgerRows, &state.balance, &state.gross, &state.allocated, &state.version, &state.processed)
				if err != nil {
					t.Fatalf("Read maintenance %q financial state: %v", scenario.name, err)
				}
				return state
			}
			state := readState()
			var matched *outcome
			for index := range scenario.wantOutcomes {
				expected := &scenario.wantOutcomes[index]
				if expected.state == state && expected.wantWorkerSQLState == workerSQLState {
					matched = expected
					break
				}
			}
			if matched == nil {
				t.Fatalf("Maintenance %q state=%+v worker SQLSTATE=%q; want one of %+v", scenario.name, state, workerSQLState, scenario.wantOutcomes)
			}
			if worker.worked != matched.wantWorkerWorked {
				t.Errorf("Maintenance %q worker worked=%t; want worked=%t", scenario.name, worker.worked, matched.wantWorkerWorked)
			}

			worked, err := store.ProcessBatch(ctx)
			if err != nil {
				t.Fatalf("Retry after maintenance %q: %v", scenario.name, err)
			}
			if worked != matched.wantRetryWorked {
				t.Errorf("Retry after maintenance %q worked=%t; want %t", scenario.name, worked, matched.wantRetryWorked)
			}
			if final := readState(); final != input.wantFinalState {
				t.Errorf("Final state after maintenance %q=%+v; want %+v", scenario.name, final, input.wantFinalState)
			}
		})
	}
}

// testOldestLockedCustomer exposes the global pending-candidate bottleneck.
// Each isolated scenario blocks one customer, verifies the exceptional timeout
// or adjacent unlocked first customer, then requires complete accounting once
// the blocker ends without changes to credit or quarantine state.
func testOldestLockedCustomer(t *testing.T) {
	cases := []struct {
		name, blockedCustomer string
		wantError             error
		wantWorked            bool
		wantProcessed         []bool
		wantRatings           int
		wantAfterReleaseWork  []bool
	}{
		{name: "locked oldest customer delays an unrelated ready customer", blockedCustomer: "acme", wantError: context.DeadlineExceeded, wantWorked: false, wantProcessed: []bool{false, false}, wantRatings: 0, wantAfterReleaseWork: []bool{true, true, false}},
		{name: "locked later customer does not delay the oldest unlocked customer", blockedCustomer: "cyberdyne", wantError: nil, wantWorked: true, wantProcessed: []bool{true, false}, wantRatings: 1, wantAfterReleaseWork: []bool{true, false}},
	}
	inputs := []struct {
		event            usage.Event
		receivedAt       string
		wantFinalGross   string
		wantFinalBalance string
		wantFinalVersion int64
	}{
		{event: usage.Event{Source: "oldest-lock", EventID: "first", SchemaVersion: 1, CustomerID: "acme", SandboxID: "sandbox", Metric: "cpu_seconds", PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z"), Units: 1_000_000}, receivedAt: "2026-10-10T13:00:00Z", wantFinalGross: "4000000", wantFinalBalance: "0", wantFinalVersion: 1},
		{event: usage.Event{Source: "oldest-lock", EventID: "second", SchemaVersion: 1, CustomerID: "cyberdyne", SandboxID: "sandbox", Metric: "cpu_seconds", PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z"), Units: 1_000_000}, receivedAt: "2026-10-10T13:00:01Z", wantFinalGross: "5000000", wantFinalBalance: "0", wantFinalVersion: 1},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			store := newPostgresStore(t, pool, postgres.Config{})
			for _, input := range inputs {
				if err := store.InsertBatch(ctx, []usage.Event{input.event}, parseBillingTime(t, input.receivedAt)); err != nil {
					t.Fatalf("Insert pending customer=%s: %v", input.event.CustomerID, err)
				}
			}
			blocker, err := pool.Begin(ctx)
			if err != nil {
				t.Fatalf("Begin oldest-customer blocker: %v", err)
			}
			defer blocker.Rollback(context.Background())
			if _, err := blocker.Exec(ctx, "SELECT customer_id FROM customer_billing_state WHERE customer_id=$1 FOR UPDATE", scenario.blockedCustomer); err != nil {
				t.Fatalf("Lock customer=%s: %v", scenario.blockedCustomer, err)
			}
			attempt, stopAttempt := context.WithTimeout(ctx, time.Second)
			defer stopAttempt()

			worked, err := store.ProcessBatch(attempt)

			if !errors.Is(err, scenario.wantError) {
				t.Fatalf("ProcessBatch blocked customer=%s error=%v; want %v", scenario.blockedCustomer, err, scenario.wantError)
			}
			if worked != scenario.wantWorked {
				t.Errorf("ProcessBatch blocked customer=%s worked=%t; want %t", scenario.blockedCustomer, worked, scenario.wantWorked)
			}
			for index, input := range inputs {
				var processed bool
				var processingError *string
				if err := pool.QueryRow(ctx, "SELECT processed_at IS NOT NULL,processing_error FROM usage_inbox WHERE source=$1 AND event_id=$2", input.event.Source, input.event.EventID).Scan(&processed, &processingError); err != nil {
					t.Fatalf("Read blocked customer=%s event=%s: %v", scenario.blockedCustomer, input.event.EventID, err)
				}
				if processed != scenario.wantProcessed[index] || processingError != nil {
					t.Errorf("Blocked customer=%s event=%s processed=%t processing_error=%v; want processed=%t and no quarantine", scenario.blockedCustomer, input.event.EventID, processed, processingError, scenario.wantProcessed[index])
				}
			}
			var ratings int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM usage_ratings").Scan(&ratings); err != nil {
				t.Fatalf("Read ratings after blocking: %v", err)
			}
			if ratings != scenario.wantRatings {
				t.Errorf("Blocked customer=%s ratings=%d; want %d", scenario.blockedCustomer, ratings, scenario.wantRatings)
			}
			if err := blocker.Commit(ctx); err != nil {
				t.Fatalf("Release customer=%s: %v", scenario.blockedCustomer, err)
			}

			for index, wantWorked := range scenario.wantAfterReleaseWork {
				worked, err := store.ProcessBatch(ctx)
				if err != nil {
					t.Fatalf("ProcessBatch after releasing %s step=%d: %v", scenario.blockedCustomer, index, err)
				}
				if worked != wantWorked {
					t.Errorf("ProcessBatch after releasing %s step=%d worked=%t; want %t", scenario.blockedCustomer, index, worked, wantWorked)
				}
			}
			for _, input := range inputs {
				var gross, balance string
				var version int64
				err := pool.QueryRow(ctx, `SELECT (SELECT gross_charge_microcents::text FROM monthly_usage WHERE customer_id=$1),credit_balance_microcents::text,state_version FROM customer_billing_state WHERE customer_id=$1`, input.event.CustomerID).Scan(&gross, &balance, &version)
				if err != nil {
					t.Fatalf("Read final customer=%s after releasing %s: %v", input.event.CustomerID, scenario.blockedCustomer, err)
				}
				if gross != input.wantFinalGross || balance != input.wantFinalBalance || version != input.wantFinalVersion {
					t.Errorf("Final customer=%s gross=%s balance=%s version=%d; want %s,%s,%d", input.event.CustomerID, gross, balance, version, input.wantFinalGross, input.wantFinalBalance, input.wantFinalVersion)
				}
			}
		})
	}
}
