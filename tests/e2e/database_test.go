//go:build integration

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// query reads committed state through a connection independent of the API and
// worker, keeping SQL errors separate from expected financial value assertions.
func (e *environment) query(t *testing.T, sql string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), observationTimeout)
	defer cancel()
	var value string
	if err := e.pool.QueryRow(ctx, sql).Scan(&value); err != nil {
		t.Fatalf("Read persisted state with %s: %v", sql, err)
	}
	return value
}

// assertSQL compares a declared SQL observation with literal expected output;
// it records both the query and result without calling financial production code.
func (e *environment) assertSQL(t *testing.T, name, sql, want string) {
	t.Helper()
	actual := e.query(t, sql)
	e.save(t, "database.log", fmt.Sprintf("%s\n%s\nactual=%s\nwant=%s\n", name, sql, actual, want), true)
	if actual != want {
		t.Fatalf("%s: SQL %s returned %s; want %s", name, sql, actual, want)
	}
}

// awaitSQL polls a committed-state predicate with real time, bounding eventual
// accounting without guessing the winner through a fixed sleep.
func (e *environment) awaitSQL(t *testing.T, name, sql, want string) {
	t.Helper()
	deadline := time.Now().Add(observationTimeout)
	for {
		actual := e.query(t, sql)
		if actual == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: SQL %s returned %s; want %s before deadline", name, sql, actual, want)
		}
		time.Sleep(observationInterval)
	}
}

// assertJSON compares literal database arrays including every declared field,
// treating object key order as irrelevant while retaining exact integer values.
func assertJSON(t *testing.T, name, actual, want string) {
	t.Helper()
	got, err := decodeJSON(actual)
	if err != nil {
		t.Fatalf("Decode %s actual %s: %v", name, actual, err)
	}
	expected, err := decodeJSON(want)
	if err != nil {
		t.Fatalf("Decode %s expectation %s: %v", name, want, err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("%s: actual %s; want %s", name, actual, want)
	}
}

// snapshot freezes complete financial rows, original audit timestamps, catalog,
// migration records and invoice snapshots for retry/restart comparisons.
func (e *environment) snapshot(t *testing.T) string {
	t.Helper()
	tables := []string{"schema_migrations", "customers", "metrics", "price_versions", "addons", "usage_inbox", "customer_billing_state", "credit_entries", "addon_subscriptions", "addon_cancellations", "usage_ratings", "rated_usage_groups", "monthly_usage", "invoices", "closed_billing_months", "invoiced_usage_groups", "api_idempotency_operations"}
	state := map[string]json.RawMessage{}
	for _, table := range tables {
		sql := fmt.Sprintf(`SELECT COALESCE(jsonb_agg(row ORDER BY row::text),'[]'::jsonb)::text FROM (SELECT to_jsonb(t) AS row FROM %s t) data`, pgx.Identifier{table}.Sanitize())
		state[table] = json.RawMessage(e.query(t, sql))
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("Encode persisted snapshot: %v", err)
	}
	return string(data)
}

// assertInvariants checks independently persisted conservation and one rating
// per completed receipt after the public workflow's literal HTTP assertions.
func (e *environment) assertInvariants(t *testing.T) {
	t.Helper()
	checks := []struct{ name, inputSQL, wantResult string }{
		{name: "credit ledger equals every account balance", inputSQL: `SELECT count(*)::text FROM customer_billing_state s WHERE credit_balance_microcents<>(SELECT COALESCE(sum(amount_microcents),0) FROM credit_entries e WHERE e.customer_id=s.customer_id)`, wantResult: "0"},
		{name: "processed receipts have exactly one rating and rejected receipts have none", inputSQL: `SELECT count(*)::text FROM usage_inbox i WHERE (SELECT count(*) FROM usage_ratings r WHERE r.source=i.source AND r.event_id=i.event_id) <> CASE WHEN processed_at IS NOT NULL THEN 1 ELSE 0 END`, wantResult: "0"},
		{name: "invoice displayed lines sum to total", inputSQL: `SELECT count(*)::text FROM invoices i WHERE total_cents<>(SELECT sum((line->>'amount_cents')::bigint) FROM jsonb_array_elements(i.snapshot->'lines') line)`, wantResult: "0"},
		{name: "exact allocated credit stays within usage gross", inputSQL: `SELECT count(*)::text FROM rated_usage_groups WHERE allocated_credit_microcents<0 OR allocated_credit_microcents>exact_charge_microcents`, wantResult: "0"},
	}
	for _, check := range checks {
		e.assertSQL(t, check.name, check.inputSQL, check.wantResult)
	}
}
