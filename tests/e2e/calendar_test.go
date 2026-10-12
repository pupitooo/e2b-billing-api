//go:build integration

package e2e_test

import "testing"

// testHalfYear keeps one recurring subscription and its original command audit
// through a six-month business-clock jump. Closing March just before April is
// rejected without writes; the exact boundary publishes all six full fees once.
func testHalfYear(t *testing.T) {
	steps := []httpStep{
		{name: "buy October recurring add-on", capture: "original-pack", method: "POST", path: "/customers/acme/addons", inputBody: `{"idempotency_key":"half-year-pack","addon_name":"concurrency_pack","purchased_at":"2026-10-01"}`, wantStatus: 200, wantBody: `{"monthly_price_cents":2000,"start_month":"2026-10-01"}`},
		{name: "last March microsecond cannot close March", businessTime: "2027-03-31T23:59:59.999999Z", method: "POST", path: "/customers/acme/invoices/catch-up", inputBody: `{"idempotency_key":"six-months","through_month":"2027-03"}`, wantStatus: 422, wantBody: `{"error":{"code":"invalid_command","field":"through_month"}}`, wantUnchangedState: true},
		{name: "exact April boundary publishes October through March", capture: "six-month-publication", businessTime: "2027-04-01T00:00:00Z", method: "POST", path: "/customers/acme/invoices/catch-up", inputBody: `{"idempotency_key":"six-months","through_month":"2027-03"}`, wantStatus: 200, wantBody: `{"through_month":"2027-03","invoices":[{"month":"2026-10","number":"ACME-0001","total_cents":2000},{"month":"2026-11","number":"ACME-0002","total_cents":2000},{"month":"2026-12","number":"ACME-0003","total_cents":2000},{"month":"2027-01","number":"ACME-0004","total_cents":2000},{"month":"2027-02","number":"ACME-0005","total_cents":2000},{"month":"2027-03","number":"ACME-0006","total_cents":2000}]}`},
		{name: "replaying original purchase retains subscription audit", wantSameAs: "original-pack", method: "POST", path: "/customers/acme/addons", inputBody: `{"idempotency_key":"half-year-pack","addon_name":"concurrency_pack","purchased_at":"2026-10-01"}`, wantStatus: 200, wantBody: `{"monthly_price_cents":2000,"start_month":"2026-10-01"}`, wantUnchangedState: true},
		{name: "catch-up retry has no second financial effect", wantSameAs: "six-month-publication", method: "POST", path: "/customers/acme/invoices/catch-up", inputBody: `{"idempotency_key":"six-months","through_month":"2027-03"}`, wantStatus: 200, wantBody: `{"through_month":"2027-03"}`, wantUnchangedState: true},
		{name: "clock jump does not create credit debits or pending usage", method: "GET", path: "/customers/acme/credit", wantStatus: 200, wantBody: `{"credit_balance_microcents":"0","pending_events":0,"processing_errors":0,"state_version":7}`},
	}
	checks := []struct{ name, inputSQL, wantOutput string }{
		{name: "six full fees and one subscription without any ledger entry", inputSQL: `SELECT jsonb_build_array((SELECT count(*) FROM invoices),(SELECT sum(total_cents) FROM invoices),(SELECT count(*) FROM addon_subscriptions),(SELECT count(*) FROM credit_entries),(SELECT next_invoice_number FROM customer_billing_state WHERE customer_id='acme'))::text`, wantOutput: `[6, 12000, 1, 0, 7]`},
		{name: "original purchase command keeps October audit time", inputSQL: `SELECT created_at::text FROM api_idempotency_operations WHERE idempotency_key='half-year-pack'`, wantOutput: "2026-10-01 00:00:00+00"},
	}
	e := newEnvironment(t, "half-year")
	e.migrate(t)
	e.start(t, "2026-10-01T00:00:00Z")
	if !e.runHTTPSteps(t, steps) {
		return
	}
	for _, check := range checks {
		e.assertSQL(t, check.name, check.inputSQL, check.wantOutput)
	}
	e.assertInvariants(t)
	e.save(t, "final-state.json", e.snapshot(t), false)
}
