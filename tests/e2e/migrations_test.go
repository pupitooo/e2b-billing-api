//go:build integration

package e2e_test

import (
	"context"
	"fmt"
	"testing"
)

// testMigrations starts two fresh migration runners simultaneously, verifies
// literal assignment seeds, then requires repeated and concurrent reruns and a
// PostgreSQL restart to preserve every migration record and seeded row.
func testMigrations(t *testing.T) {
	scenario := struct {
		inputRunners          int
		inputSQL              string
		wantSeeds             string
		wantMigrationVersions string
		wantRunnerError       bool
	}{
		inputRunners: 2,
		inputSQL: `SELECT jsonb_build_object(
   'customers',(SELECT jsonb_agg(jsonb_build_array(customer_id,name,country,billing_address) ORDER BY customer_id) FROM customers),
   'prices',(SELECT jsonb_agg(jsonb_build_array(price_version_id,customer_id,metric,price_per_million_cents,effective_from) ORDER BY price_version_id) FROM price_versions),
   'metrics',(SELECT jsonb_agg(metric ORDER BY metric) FROM metrics),
   'addons',(SELECT jsonb_agg(jsonb_build_array(addon_name,monthly_price_cents) ORDER BY addon_name) FROM addons),
   'accounts',(SELECT jsonb_agg(jsonb_build_array(customer_id,credit_balance_microcents,spend_limit_cents,state_version,next_invoice_number) ORDER BY customer_id) FROM customer_billing_state),
   'activity',jsonb_build_array((SELECT count(*) FROM usage_inbox),(SELECT count(*) FROM credit_entries),(SELECT count(*) FROM addon_subscriptions),(SELECT count(*) FROM invoices)))::text`,
		wantSeeds:             `{"customers":[["acme","Acme Inc.","US","1 Market St, San Francisco, CA 94105"],["cyberdyne","Cyberdyne Systems Corporation","US","18144 El Camino Real, Sunnyvale, CA 94087"]],"prices":[["cpu-acme-2026-10-01","acme","cpu_seconds",4,"2026-10-01T00:00:00+00:00"],["cpu-default-2026-10-01",null,"cpu_seconds",5,"2026-10-01T00:00:00+00:00"],["cpu-default-2026-10-15",null,"cpu_seconds",6,"2026-10-15T00:00:00+00:00"]],"metrics":["cpu_seconds"],"addons":[["concurrency_pack",2000]],"accounts":[["acme",0,null,0,1],["cyberdyne",0,null,0,1]],"activity":[0,0,0,0]}`,
		wantMigrationVersions: `[1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13]`,
		wantRunnerError:       false,
	}
	e := newEnvironment(t, "migrations")
	runMigrationsConcurrently(t, e, scenario.inputRunners, scenario.wantRunnerError)
	e.assertSQL(t, "all migrations recorded once", `SELECT jsonb_agg(version ORDER BY version)::text FROM schema_migrations`, scenario.wantMigrationVersions)
	assertJSON(t, "literal assignment seeds", e.query(t, scenario.inputSQL), scenario.wantSeeds)
	e.assertSQL(t, "server uses UTC", `SELECT current_setting('TimeZone')`, "UTC")
	before := e.snapshot(t)
	e.migrate(t)
	runMigrationsConcurrently(t, e, scenario.inputRunners, scenario.wantRunnerError)
	e.compose(t, "restart", "postgres")
	e.compose(t, "up", "-d", "--wait", "--wait-timeout", "90", "postgres")
	if after := e.snapshot(t); after != before {
		t.Fatalf("Repeated/concurrent migration and PostgreSQL restart changed seed or migration rows: before=%s after=%s", before, after)
	}
	e.save(t, "seed-state.json", before, false)
}

// runMigrationsConcurrently gates both independent psql processes together and
// checks their actual exits on the parent goroutine; advisory locking must let
// every runner succeed without changing existing records.
func runMigrationsConcurrently(t *testing.T, e *environment, count int, wantError bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), composeTimeout)
	defer cancel()
	type result struct {
		output []byte
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, count)
	for range count {
		go func() {
			<-start
			output, err := e.command(ctx, "exec", "-T", "postgres", "psql", "-X", "-U", "e2b", "-d", "e2b_billing", "-v", "ON_ERROR_STOP=1", "-f", "/migrations/migrate.sql").CombinedOutput()
			results <- result{output: output, err: err}
		}()
	}
	close(start)
	for index := range count {
		result := <-results
		e.save(t, "migration-runners.log", fmt.Sprintf("runner=%d error=%v\n%s\n", index, result.err, result.output), true)
		if (result.err != nil) != wantError {
			t.Errorf("Concurrent migration runner %d: error=%v; wantError=%v\n%s", index, result.err, wantError, result.output)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
}
