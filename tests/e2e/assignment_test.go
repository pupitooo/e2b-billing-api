//go:build integration

package e2e_test

import (
	"fmt"
	"testing"
)

// testAssignment automates backend acceptance sections 4-7: an API outage,
// producer recovery, retained grant, worker pause, immutable receipt retries,
// complete literal invoices, independent SQL accounting, and service restart.
func testAssignment(t *testing.T) {
	scenario := struct {
		file, initialTime                     string
		clockChanges                          map[string]string
		stopWorkerBefore, restartWorkerBefore string
		compareRetryAt                        string
		wantSteps                             int
		unavailable                           producerExpectation
		status                                producerExpectation
	}{
		file: "billing-assignment", initialTime: "2026-10-31T00:00:00Z",
		clockChanges:     map[string]string{"issue Acme October invoice": "2026-11-01T00:00:00Z", "issue Acme November invoice": "2026-12-01T00:00:00Z"},
		stopWorkerBefore: "platform sends the first hourly measurements", restartWorkerBefore: "Acme has USD 21 after the first hour", compareRetryAt: "platform retries the exact first measurements", wantSteps: 24,
		unavailable: producerExpectation{wantError: true, wantRetainedError: true, wantOutput: "dial tcp", wantCompleted: 0, wantSteps: 24},
		status:      producerExpectation{wantError: false, wantRetainedError: true, wantOutput: "dial tcp", wantCompleted: 0, wantSteps: 24},
	}
	e := newEnvironment(t, scenario.file)
	e.migrate(t)
	e.start(t, scenario.initialTime)
	e.compose(t, "stop", "api")
	before := e.snapshot(t)
	e.producer(t, scenario.file, scenario.unavailable, "--max-attempts=1")
	e.producer(t, scenario.file, scenario.status, "--action=status")
	if after := e.snapshot(t); after != before {
		t.Fatal("Unavailable API changed initial financial state")
	}
	e.compose(t, "up", "-d", "--no-deps", "--wait", "--wait-timeout", "90", "api")
	plan := loadWorkflow(t, scenario.file)
	if len(plan.Steps) != scenario.wantSteps {
		t.Fatalf("Assignment steps=%d; want %d", len(plan.Steps), scenario.wantSteps)
	}
	for index, step := range plan.Steps {
		passed := t.Run(step.Name, func(t *testing.T) {
			if now, found := scenario.clockChanges[step.Name]; found {
				e.advance(t, now)
			}
			if step.Name == scenario.stopWorkerBefore {
				e.compose(t, "stop", "worker")
			}
			if step.Name == scenario.restartWorkerBefore {
				e.compose(t, "up", "-d", "--no-deps", "--wait", "--wait-timeout", "90", "worker")
			}
			var retrySnapshot string
			if step.Name == scenario.compareRetryAt {
				retrySnapshot = e.snapshot(t)
			}
			e.workflowStep(t, scenario.file, index, step, scenario.wantSteps)
			if step.Name == scenario.stopWorkerBefore {
				assertStoppedWorker(t, e)
			}
			if retrySnapshot != "" && e.snapshot(t) != retrySnapshot {
				t.Fatal("Identical usage retry changed complete financial state or original receipt metadata")
			}
		})
		if !passed {
			return
		}
	}
	assertAssignmentState(t, e)
	e.assertInvariants(t)
	e.producer(t, scenario.file, producerExpectation{wantCompleted: 24, wantSteps: 24}, "--action=status")
	e.producer(t, scenario.file, producerExpectation{wantCompleted: 24, wantSteps: 24})
	verifyServiceRestart(t, e)
}

// assertStoppedWorker checks the manual checkpoint's two committed receipts,
// no ratings or credit debits, and the unchanged literal account balances before
// the real worker executable is restarted.
func assertStoppedWorker(t *testing.T, e *environment) {
	t.Helper()
	checks := []struct{ name, inputSQL, wantOutput string }{
		{name: "two receipts are pending without ratings or errors", inputSQL: `SELECT jsonb_build_array(count(*),count(processed_at),count(processing_error),(SELECT count(*) FROM usage_ratings))::text FROM usage_inbox`, wantOutput: `[2, 0, 0, 0]`},
		{name: "no usage debit exists before worker restart", inputSQL: `SELECT jsonb_build_array(count(*),sum(amount_microcents))::text FROM credit_entries`, wantOutput: `[1, 2500000000]`},
		{name: "accounts retain grant purchase and limit versions", inputSQL: `SELECT jsonb_agg(jsonb_build_array(customer_id,credit_balance_microcents,state_version) ORDER BY customer_id)::text FROM customer_billing_state`, wantOutput: `[["acme", 2500000000, 2], ["cyberdyne", 0, 1]]`},
	}
	for _, check := range checks {
		e.assertSQL(t, check.name, check.inputSQL, check.wantOutput)
	}
}

// assertAssignmentState independently compares every group, monthly total,
// account, ledger amount, price routing and persisted invoice buyer/number with
// the literal financial tables in manual acceptance section 5.
func assertAssignmentState(t *testing.T, e *environment) {
	t.Helper()
	checks := []struct{ name, inputSQL, wantOutput string }{
		{name: "exact group units gross credit and booked cents", inputSQL: `SELECT jsonb_agg(jsonb_build_array(customer_id,price_version_id,usage_month,billing_month,total_units,exact_charge_microcents,allocated_credit_microcents,booked_charge_cents) ORDER BY customer_id,billing_month,usage_month,price_version_id)::text FROM rated_usage_groups`, wantOutput: `[["acme", "cpu-acme-2026-10-01", "2026-10-01", "2026-10-01", 300000000, 1200000000, 1200000000, 1200], ["acme", "cpu-acme-2026-10-01", "2026-10-01", "2026-11-01", 50000000, 200000000, 200000000, 200], ["acme", "cpu-acme-2026-10-01", "2026-11-01", "2026-11-01", 100000000, 400000000, 400000000, 400], ["cyberdyne", "cpu-default-2026-10-01", "2026-10-01", "2026-10-01", 123456789, 617283945, 0, 617], ["cyberdyne", "cpu-default-2026-10-15", "2026-10-01", "2026-10-01", 200000000, 1200000000, 0, 1200]]`},
		{name: "original usage month gross includes late consumption", inputSQL: `SELECT jsonb_agg(jsonb_build_array(customer_id,usage_month,gross_charge_microcents) ORDER BY customer_id,usage_month)::text FROM monthly_usage`, wantOutput: `[["acme", "2026-10-01", 1400000000], ["acme", "2026-11-01", 400000000], ["cyberdyne", "2026-10-01", 1817283945]]`},
		{name: "literal balances limits versions and numbering", inputSQL: `SELECT jsonb_agg(jsonb_build_array(customer_id,credit_balance_microcents,spend_limit_cents,state_version,next_invoice_number) ORDER BY customer_id)::text FROM customer_billing_state`, wantOutput: `[["acme", 700000000, null, 8, 3], ["cyberdyne", 0, 1500, 4, 2]]`},
		{name: "one grant and four exact usage debits", inputSQL: `SELECT jsonb_agg(amount_microcents ORDER BY amount_microcents)::text FROM credit_entries`, wantOutput: `[-800000000, -400000000, -400000000, -200000000, 2500000000]`},
		{name: "original receipt and processing clocks follow declared phases", inputSQL: `SELECT jsonb_agg(jsonb_build_array(event_id,received_at,processed_at) ORDER BY event_id)::text FROM usage_inbox`, wantOutput: `[["acme-first", "2026-10-31T00:00:00+00:00", "2026-10-31T00:00:00+00:00"], ["acme-late", "2026-11-01T00:00:00+00:00", "2026-11-01T00:00:00+00:00"], ["acme-november", "2026-11-01T00:00:00+00:00", "2026-11-01T00:00:00+00:00"], ["acme-second", "2026-10-31T00:00:00+00:00", "2026-10-31T00:00:00+00:00"], ["cyberdyne-first", "2026-10-31T00:00:00+00:00", "2026-10-31T00:00:00+00:00"], ["cyberdyne-second", "2026-10-31T00:00:00+00:00", "2026-10-31T00:00:00+00:00"]]`},
		{name: "all receipts rated once with five frozen groups", inputSQL: `SELECT jsonb_build_array((SELECT count(*) FROM usage_inbox),(SELECT count(*) FROM usage_ratings),(SELECT count(*) FROM usage_inbox WHERE processed_at IS NOT NULL AND processing_error IS NULL),(SELECT count(*) FROM invoices),(SELECT count(*) FROM closed_billing_months),(SELECT count(*) FROM invoiced_usage_groups),(SELECT count(*) FROM addon_subscriptions))::text`, wantOutput: `[6, 6, 6, 3, 3, 5, 1]`},
		{name: "persisted buyer snapshots numbers and totals", inputSQL: `SELECT jsonb_agg(jsonb_build_array(customer_id,billing_month,invoice_number,total_cents,snapshot->>'customer_name',snapshot->>'country',snapshot->>'billing_address') ORDER BY customer_id,billing_month)::text FROM invoices`, wantOutput: `[["acme", "2026-10-01", "ACME-0001", 2000, "Acme Inc.", "US", "1 Market St, San Francisco, CA 94105"], ["acme", "2026-11-01", "ACME-0002", 2000, "Acme Inc.", "US", "1 Market St, San Francisco, CA 94105"], ["cyberdyne", "2026-10-01", "CYBERDYNE-0001", 1817, "Cyberdyne Systems Corporation", "US", "18144 El Camino Real, Sunnyvale, CA 94087"]]`},
	}
	for _, check := range checks {
		e.assertSQL(t, check.name, check.inputSQL, check.wantOutput)
	}
}

// verifyServiceRestart retains complete HTTP snapshots, financial rows and
// producer checkpoints, restarts PostgreSQL/API/worker, then rereads and retries
// every issued invoice without any audit or numbering changes.
func verifyServiceRestart(t *testing.T, e *environment) {
	t.Helper()
	paths := []string{"/customers/acme/invoices/2026-10", "/customers/acme/invoices/2026-11", "/customers/cyberdyne/invoices/2026-10", "/customers/acme/credit", "/customers/cyberdyne/months/2026-10/limit-status", "/customers/cyberdyne/limit-status"}
	responses := make(map[string]string)
	for _, path := range paths {
		responses[path] = e.request(t, httpStep{name: "before restart", method: "GET", path: path, wantStatus: 200}).body
	}
	before := e.snapshot(t)
	e.save(t, "before-restart.json", before, false)
	e.captureLogs(t)
	e.compose(t, "restart", "postgres", "api", "worker")
	e.compose(t, "up", "-d", "--no-deps", "--wait", "--wait-timeout", "90", "postgres", "api", "worker")
	for _, path := range paths {
		actual := e.request(t, httpStep{name: "after restart", method: "GET", path: path, wantStatus: 200})
		assertJSON(t, "restart preserves "+path, actual.body, responses[path])
	}
	for _, invoice := range []struct{ customer, month string }{{customer: "acme", month: "2026-10"}, {customer: "acme", month: "2026-11"}, {customer: "cyberdyne", month: "2026-10"}} {
		path := "/customers/" + invoice.customer + "/invoices"
		actual := e.request(t, httpStep{name: "closing retry after restart", method: "POST", path: path, inputBody: fmt.Sprintf(`{"month":%q}`, invoice.month), wantStatus: 200})
		assertJSON(t, "immutable closing retry "+path, actual.body, responses[path+"/"+invoice.month])
	}
	e.producer(t, "billing-assignment", producerExpectation{wantCompleted: 24, wantSteps: 24}, "--action=status")
	e.producer(t, "billing-assignment", producerExpectation{wantCompleted: 24, wantSteps: 24})
	if after := e.snapshot(t); after != before {
		t.Fatalf("Service restart/read/retry changed persisted state: before=%s after=%s", before, after)
	}
}
