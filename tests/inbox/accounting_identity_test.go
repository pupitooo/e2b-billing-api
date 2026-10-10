//go:build integration

package inbox_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/usage"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type accountingIdentityState struct {
	groupIDs       string
	creditIDs      string
	units          string
	grossTicks     string
	allocatedTicks string
	monthlyTicks   string
	balanceTicks   string
	ratings        int
	pending        int
	stateVersion   int64
	groupSequence  int64
	creditSequence int64
}

// assertAccountingIdentityState reads identities, exact financial effects,
// pending receipts, and sequence positions from one private scenario schema.
func assertAccountingIdentityState(t *testing.T, pool *pgxpool.Pool, want accountingIdentityState) {
	t.Helper()
	var actual accountingIdentityState
	err := pool.QueryRow(context.Background(), `SELECT
		(SELECT COALESCE(string_agg(group_id,',' ORDER BY group_id),'') FROM rated_usage_groups),
		(SELECT COALESCE(string_agg(credit_entry_id,',' ORDER BY credit_entry_id),'') FROM credit_entries),
		(SELECT COALESCE(sum(total_units),0)::text FROM rated_usage_groups),
		(SELECT COALESCE(sum(exact_charge_ticks),0)::text FROM rated_usage_groups),
		(SELECT COALESCE(sum(allocated_credit_ticks),0)::text FROM rated_usage_groups),
		(SELECT COALESCE(sum(gross_charge_ticks),0)::text FROM monthly_usage),
		(SELECT sum(credit_balance_ticks)::text FROM customer_billing_state),
		(SELECT count(*) FROM usage_ratings),
		(SELECT count(*) FROM usage_inbox WHERE processed_at IS NULL),
		(SELECT sum(state_version)::bigint FROM customer_billing_state),
		(SELECT CASE WHEN is_called THEN last_value ELSE 0 END FROM rated_usage_group_id_seq),
		(SELECT CASE WHEN is_called THEN last_value ELSE 0 END FROM credit_entry_id_seq)`).
		Scan(&actual.groupIDs, &actual.creditIDs, &actual.units, &actual.grossTicks, &actual.allocatedTicks,
			&actual.monthlyTicks, &actual.balanceTicks, &actual.ratings, &actual.pending, &actual.stateVersion,
			&actual.groupSequence, &actual.creditSequence)
	if err != nil {
		t.Fatal(err)
	}

	if actual != want {
		t.Errorf("Accounting identities and financial state=%+v; want %+v", actual, want)
	}
}

type accountingUsageStep struct {
	name          string
	customer      string
	eventID       string
	units         int64
	wantProcessed bool
}

// testAccountingGroupIdentities accumulates repeated and split receipts by the
// financial tuple. Earlier IDs are reused without sequence allocation or lost
// cumulative ticks, and replay leaves both ledger and sequence state unchanged.
func testAccountingGroupIdentities(t *testing.T) {
	cases := []struct {
		name      string
		setupSQL  string
		steps     []accountingUsageStep
		wantState accountingIdentityState
	}{
		{
			name:     "split usage and replay reuse one group",
			setupSQL: "UPDATE customer_billing_state SET credit_balance_ticks=1000000 WHERE customer_id='acme'",
			steps: []accountingUsageStep{
				{name: "first receipt", customer: "acme", eventID: "one", units: 60_000, wantProcessed: true},
				{name: "second receipt", customer: "acme", eventID: "two", units: 60_000, wantProcessed: true},
				{name: "identical replay", customer: "acme", eventID: "one", units: 60_000, wantProcessed: false},
			},
			wantState: accountingIdentityState{groupIDs: "grp_1", creditIDs: "crd_1,crd_2", units: "120000", grossTicks: "480000",
				allocatedTicks: "480000", monthlyTicks: "480000", balanceTicks: "520000", ratings: 2, stateVersion: 2, groupSequence: 1, creditSequence: 2},
		},
		{
			name:     "different customers share the sequence",
			setupSQL: "UPDATE customer_billing_state SET credit_balance_ticks=1000000",
			steps: []accountingUsageStep{
				{name: "override group", customer: "acme", eventID: "one", units: 60_000, wantProcessed: true},
				{name: "default group", customer: "cyberdyne", eventID: "two", units: 60_000, wantProcessed: true},
			},
			wantState: accountingIdentityState{groupIDs: "grp_1,grp_2", creditIDs: "crd_1,crd_2", units: "120000", grossTicks: "540000",
				allocatedTicks: "540000", monthlyTicks: "540000", balanceTicks: "1460000", ratings: 2, stateVersion: 2, groupSequence: 2, creditSequence: 2},
		},
		{
			name: "earlier group and grant keep their IDs",
			setupSQL: `INSERT INTO rated_usage_groups VALUES
				('group/earlier','acme','cpu-acme-2026-10-01','cpu_seconds','2026-10-01','2026-10-01',60000,240000,0,0);
				INSERT INTO monthly_usage VALUES ('acme','2026-10-01',240000);
				INSERT INTO credit_entries VALUES ('grant/earlier','acme','grant/earlier',NULL,1000000,'2026-10-10T00:00:00Z');
				UPDATE customer_billing_state SET credit_balance_ticks=1000000,state_version=2 WHERE customer_id='acme'`,
			steps: []accountingUsageStep{
				{name: "accumulate earlier group", customer: "acme", eventID: "one", units: 60_000, wantProcessed: true},
				{name: "identical replay", customer: "acme", eventID: "one", units: 60_000, wantProcessed: false},
			},
			wantState: accountingIdentityState{groupIDs: "group/earlier", creditIDs: "crd_1,grant/earlier", units: "120000", grossTicks: "480000",
				allocatedTicks: "240000", monthlyTicks: "480000", balanceTicks: "760000", ratings: 1, stateVersion: 3, creditSequence: 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			if _, err := pool.Exec(ctx, tc.setupSQL); err != nil {
				t.Fatal(err)
			}
			producer := inbox.NewPostgres(pool, time.Second)
			store := billing.NewStore(pool)

			for _, step := range tc.steps {
				event := usage.Event{Source: "identity-test", EventID: step.eventID, SchemaVersion: 1, CustomerID: step.customer,
					SandboxID: "sandbox", Metric: "cpu_seconds", Units: step.units,
					PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z")}
				if err := producer.InsertBatch(ctx, []usage.Event{event}, event.PeriodEnd); err != nil {
					t.Fatalf("InsertBatch(%s): %v", step.name, err)
				}
				processed, err := store.ProcessBatch(ctx)
				if err != nil {
					t.Fatalf("ProcessBatch(%s): %v", step.name, err)
				}
				if processed != step.wantProcessed {
					t.Fatalf("ProcessBatch(%s)=%t; want %t", step.name, processed, step.wantProcessed)
				}
			}

			assertAccountingIdentityState(t, pool, tc.wantState)
		})
	}
}

// testAccountingIdentityConcurrency processes two customers through eight workers
// and checks distinct sequence IDs, grouping, exact balances, and replay together.
func testAccountingIdentityConcurrency(t *testing.T) {
	scenario := struct {
		workers   int
		setupSQL  string
		events    []accountingUsageStep
		wantState accountingIdentityState
	}{
		workers: 8, setupSQL: "UPDATE customer_billing_state SET credit_balance_ticks=10000000",
		events: []accountingUsageStep{
			{customer: "acme", eventID: "one", units: 60_000}, {customer: "cyberdyne", eventID: "two", units: 60_000},
			{customer: "acme", eventID: "three", units: 60_000}, {customer: "cyberdyne", eventID: "four", units: 60_000},
			{customer: "acme", eventID: "five", units: 60_000}, {customer: "cyberdyne", eventID: "six", units: 60_000},
			{customer: "acme", eventID: "seven", units: 60_000}, {customer: "cyberdyne", eventID: "eight", units: 60_000},
		},
		wantState: accountingIdentityState{groupIDs: "grp_1,grp_2", creditIDs: "crd_1,crd_2,crd_3,crd_4,crd_5,crd_6,crd_7,crd_8",
			units: "480000", grossTicks: "2160000", allocatedTicks: "2160000", monthlyTicks: "2160000", balanceTicks: "17840000",
			ratings: 8, stateVersion: 8, groupSequence: 2, creditSequence: 8},
	}
	pool := billingDatabase(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, scenario.setupSQL); err != nil {
		t.Fatal(err)
	}
	events := make([]usage.Event, len(scenario.events))
	for index, input := range scenario.events {
		events[index] = usage.Event{Source: "concurrent-identities", EventID: input.eventID, SchemaVersion: 1, CustomerID: input.customer,
			SandboxID: "sandbox", Metric: "cpu_seconds", Units: input.units,
			PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z")}
	}
	producer := inbox.NewPostgres(pool, time.Second)
	if err := producer.InsertBatch(ctx, events, parseBillingTime(t, "2026-10-10T13:00:00Z")); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	results := make(chan error, scenario.workers)

	for range scenario.workers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			store := billing.NewStore(pool)
			for {
				processed, err := store.ProcessBatch(ctx)
				if err != nil || !processed {
					results <- err
					return
				}
			}
		}()
	}
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("Concurrent sequence processing: %v", err)
		}
	}
	if err := producer.InsertBatch(ctx, events, parseBillingTime(t, "2026-10-11T00:00:00Z")); err != nil {
		t.Fatal(err)
	}
	processed, err := billing.NewStore(pool).ProcessBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if processed {
		t.Fatal("Replayed receipts were processed again; want no pending work")
	}

	assertAccountingIdentityState(t, pool, scenario.wantState)
}

// testAccountingSequenceExhaustion verifies the final usable sequence value and
// the next rejected allocation. A failed debit allocation rolls back its new
// group, rating, projections, and receipt completion with the balance untouched.
func testAccountingSequenceExhaustion(t *testing.T) {
	cases := []struct {
		name         string
		sequenceSQL  string
		wantError    bool
		wantSQLState string
		wantState    accountingIdentityState
	}{
		{
			name: "last group number is usable", sequenceSQL: "ALTER SEQUENCE rated_usage_group_id_seq MAXVALUE 2; SELECT setval('rated_usage_group_id_seq',2,false)",
			wantState: accountingIdentityState{groupIDs: "grp_2", creditIDs: "crd_1", units: "60000", grossTicks: "240000",
				allocatedTicks: "240000", monthlyTicks: "240000", balanceTicks: "760000", ratings: 1, stateVersion: 1, groupSequence: 2, creditSequence: 1},
		},
		{
			name:        "exhausted group sequence has no financial effects",
			sequenceSQL: "ALTER SEQUENCE rated_usage_group_id_seq MAXVALUE 2; SELECT setval('rated_usage_group_id_seq',2,true)",
			wantError:   true, wantSQLState: "2200H",
			wantState: accountingIdentityState{units: "0", grossTicks: "0", allocatedTicks: "0", monthlyTicks: "0", balanceTicks: "1000000", pending: 1, groupSequence: 2},
		},
		{
			name:        "exhausted debit sequence rolls back its group",
			sequenceSQL: "ALTER SEQUENCE credit_entry_id_seq MAXVALUE 2; SELECT setval('credit_entry_id_seq',2,true)",
			wantError:   true, wantSQLState: "2200H",
			wantState: accountingIdentityState{units: "0", grossTicks: "0", allocatedTicks: "0", monthlyTicks: "0", balanceTicks: "1000000", pending: 1, groupSequence: 1, creditSequence: 2},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			if _, err := pool.Exec(ctx, "UPDATE customer_billing_state SET credit_balance_ticks=1000000 WHERE customer_id='acme'"); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, tc.sequenceSQL); err != nil {
				t.Fatal(err)
			}
			event := usage.Event{Source: "exhaustion", EventID: "one", SchemaVersion: 1, CustomerID: "acme", SandboxID: "sandbox",
				Metric: "cpu_seconds", Units: 60_000, PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z")}
			if err := inbox.NewPostgres(pool, time.Second).InsertBatch(ctx, []usage.Event{event}, event.PeriodEnd); err != nil {
				t.Fatal(err)
			}

			_, err := billing.NewStore(pool).ProcessBatch(ctx)

			if (err != nil) != tc.wantError {
				t.Fatalf("ProcessBatch with %s: error=%v; wantError=%t", tc.sequenceSQL, err, tc.wantError)
			}
			if tc.wantError {
				var databaseError *pgconn.PgError
				if !errors.As(err, &databaseError) || databaseError.Code != tc.wantSQLState {
					t.Fatalf("ProcessBatch error=%v; want SQLSTATE=%s", err, tc.wantSQLState)
				}
			}
			assertAccountingIdentityState(t, pool, tc.wantState)
		})
	}
}

// testAccountingIdentityRollback fails completion after allocating both IDs,
// verifies atomic financial rollback, and retries with fresh sequence numbers.
func testAccountingIdentityRollback(t *testing.T) {
	cases := []struct {
		name         string
		setupSQL     string
		wantError    bool
		wantSQLState string
		wantState    accountingIdentityState
	}{
		{
			name:      "completion failure consumes numbers without financial effects",
			setupSQL:  "ALTER TABLE usage_inbox ADD CONSTRAINT identity_completion_failure CHECK (processed_at IS NULL)",
			wantError: true, wantSQLState: "23514",
			wantState: accountingIdentityState{units: "0", grossTicks: "0", allocatedTicks: "0", monthlyTicks: "0",
				balanceTicks: "1000000", pending: 1, groupSequence: 1, creditSequence: 1},
		},
		{
			name:     "retry uses new numbers and accounts once",
			setupSQL: "ALTER TABLE usage_inbox DROP CONSTRAINT identity_completion_failure",
			wantState: accountingIdentityState{groupIDs: "grp_2", creditIDs: "crd_2", units: "60000", grossTicks: "240000",
				allocatedTicks: "240000", monthlyTicks: "240000", balanceTicks: "760000", ratings: 1, stateVersion: 1, groupSequence: 2, creditSequence: 2},
		},
	}
	pool := billingDatabase(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "UPDATE customer_billing_state SET credit_balance_ticks=1000000 WHERE customer_id='acme'"); err != nil {
		t.Fatal(err)
	}
	event := usage.Event{Source: "identity-rollback", EventID: "one", SchemaVersion: 1, CustomerID: "acme", SandboxID: "sandbox",
		Metric: "cpu_seconds", Units: 60_000, PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z")}
	if err := inbox.NewPostgres(pool, time.Second).InsertBatch(ctx, []usage.Event{event}, event.PeriodEnd); err != nil {
		t.Fatal(err)
	}
	store := billing.NewStore(pool)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, tc.setupSQL); err != nil {
				t.Fatal(err)
			}

			_, err := store.ProcessBatch(ctx)

			if (err != nil) != tc.wantError {
				t.Fatalf("ProcessBatch(%s): error=%v; wantError=%t", tc.name, err, tc.wantError)
			}
			if tc.wantError {
				var databaseError *pgconn.PgError
				if !errors.As(err, &databaseError) || databaseError.Code != tc.wantSQLState {
					t.Fatalf("ProcessBatch(%s): error=%v; want SQLSTATE=%s", tc.name, err, tc.wantSQLState)
				}
			}
			assertAccountingIdentityState(t, pool, tc.wantState)
		})
	}
}

// testCreditEntryIdentities allocates numbers only for new grants. Identical
// retries, changed-content conflicts, earlier grant IDs, and exhausted sequences
// keep ledger effects and account balances consistent in private schemas.
func testCreditEntryIdentities(t *testing.T) {
	type grantStep struct {
		name         string
		customer     string
		key          string
		cents        int64
		wantError    error
		wantSQLState string
	}
	cases := []struct {
		name      string
		setupSQL  string
		steps     []grantStep
		wantState accountingIdentityState
	}{
		{
			name: "new grants across customers and identical retry",
			steps: []grantStep{
				{name: "first customer", customer: "acme", key: "one", cents: 1},
				{name: "other customer", customer: "cyberdyne", key: "one", cents: 1},
				{name: "retry first grant", customer: "acme", key: "one", cents: 1},
				{name: "changed-content conflict", customer: "acme", key: "one", cents: 2, wantError: billing.ErrConflict},
			},
			wantState: accountingIdentityState{creditIDs: "crd_1,crd_2", units: "0", grossTicks: "0", allocatedTicks: "0",
				monthlyTicks: "0", balanceTicks: "2000000", stateVersion: 2, creditSequence: 2},
		},
		{
			name: "earlier grant ID survives replay",
			setupSQL: `INSERT INTO credit_entries VALUES ('grant/earlier','acme','grant/one',NULL,1000000,'2026-10-01T00:00:00Z');
				UPDATE customer_billing_state SET credit_balance_ticks=1000000,state_version=1 WHERE customer_id='acme'`,
			steps: []grantStep{
				{name: "replay earlier grant", customer: "acme", key: "one", cents: 1},
				{name: "new grant", customer: "acme", key: "two", cents: 1},
			},
			wantState: accountingIdentityState{creditIDs: "crd_1,grant/earlier", units: "0", grossTicks: "0", allocatedTicks: "0",
				monthlyTicks: "0", balanceTicks: "2000000", stateVersion: 2, creditSequence: 1},
		},
		{
			name:     "last credit number and exhausted allocation",
			setupSQL: "ALTER SEQUENCE credit_entry_id_seq MAXVALUE 2; SELECT setval('credit_entry_id_seq',2,false)",
			steps: []grantStep{
				{name: "last usable number", customer: "acme", key: "one", cents: 1},
				{name: "retry still succeeds", customer: "acme", key: "one", cents: 1},
				{name: "exhaustion", customer: "acme", key: "two", cents: 1, wantSQLState: "2200H"},
			},
			wantState: accountingIdentityState{creditIDs: "crd_2", units: "0", grossTicks: "0", allocatedTicks: "0",
				monthlyTicks: "0", balanceTicks: "1000000", stateVersion: 1, creditSequence: 2},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			if tc.setupSQL != "" {
				if _, err := pool.Exec(ctx, tc.setupSQL); err != nil {
					t.Fatal(err)
				}
			}
			store := billing.NewStore(pool)

			for _, step := range tc.steps {
				err := store.GrantCredit(ctx, step.customer, billing.CreditGrant{IdempotencyKey: step.key, AmountCents: step.cents,
					RecordedAt: parseBillingTime(t, "2026-10-01T00:00:00Z")})
				if step.wantSQLState != "" {
					var databaseError *pgconn.PgError
					if !errors.As(err, &databaseError) || databaseError.Code != step.wantSQLState {
						t.Fatalf("GrantCredit(%s): error=%v; want SQLSTATE=%s", step.name, err, step.wantSQLState)
					}
				} else if !errors.Is(err, step.wantError) {
					t.Fatalf("GrantCredit(%s): error=%v; want %v", step.name, err, step.wantError)
				}
			}

			assertAccountingIdentityState(t, pool, tc.wantState)
		})
	}
}
