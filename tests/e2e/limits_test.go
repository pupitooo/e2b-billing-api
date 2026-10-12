//go:build integration

package e2e_test

import "testing"

// testCoveredLimit automates manual acceptance section 8 with the same grant,
// add-on, 400-million-unit measurement, limit changes and literal invoice values.
// Credit does not reduce gross spend and neither add-on nor closing debits credit.
func testCoveredLimit(t *testing.T) {
	steps := []httpStep{
		{name: "fresh credit", method: "GET", path: "/customers/acme/credit", wantStatus: 200, wantBody: `{"credit_balance_microcents":"0","state_version":0,"pending_events":0,"processing_errors":0}`},
		{name: "grant twenty five dollars", method: "POST", path: "/customers/acme/credits", inputBody: `{"idempotency_key":"covered-grant","amount_cents":2500}`, wantStatus: 200, wantBody: `{"amount_cents":2500,"idempotency_key":"covered-grant"}`},
		{name: "buy full recurring add-on", method: "POST", path: "/customers/acme/addons", inputBody: `{"idempotency_key":"covered-pack","addon_name":"concurrency_pack","purchased_at":"2026-10-05"}`, wantStatus: 200, wantBody: `{"monthly_price_cents":2000,"start_month":"2026-10-01"}`},
		{name: "set fifteen dollar limit", method: "POST", path: "/customers/acme/spend-limit", inputBody: `{"idempotency_key":"covered-limit","limit_cents":1500}`, wantStatus: 200, wantBody: `{"limit_cents":1500}`},
		{name: "add-on alone does not reach usage limit", method: "GET", path: "/customers/acme/months/2026-10/limit-status", wantStatus: 200, wantBody: `{"gross_charge_microcents":"0","limit_reached":false,"pending_events":0,"processing_errors":0}`},
		{name: "accept all usage even beyond the limit", method: "POST", path: "/usage/batches", inputBody: `{"events":[{"source":"acceptance-credit-limit","event_id":"covered-usage","schema_version":1,"customer_id":"acme","sandbox_id":"covered-sandbox","metric":"cpu_seconds","period_start":"2026-10-10T12:00:00Z","period_end":"2026-10-10T13:00:00Z","units":400000000}]}`, wantStatus: 202, wantBody: `{"status":"accepted"}`},
		{name: "covered sixteen dollar gross reaches limit", method: "GET", path: "/customers/acme/months/2026-10/limit-status", wantStatus: 200, wantBody: `{"gross_charge_microcents":"1600000000","limit_cents":1500,"limit_reached":true,"state_version":4,"pending_events":0,"processing_errors":0}`, await: true},
		{name: "current UTC month exposes October gross", method: "GET", path: "/customers/acme/limit-status", wantStatus: 200, wantBody: `{"month":"2026-10","gross_charge_microcents":"1600000000","limit_reached":true}`},
		{name: "new current UTC month resets gross", businessTime: "2026-11-01T00:00:00Z", method: "GET", path: "/customers/acme/limit-status", wantStatus: 200, wantBody: `{"month":"2026-11","gross_charge_microcents":"0","limit_cents":1500,"limit_reached":false,"pending_events":0,"processing_errors":0}`},
		{name: "October invoice charges only the unpaid add-on", method: "POST", path: "/customers/acme/invoices", inputBody: `{"month":"2026-10"}`, wantStatus: 200, wantBody: `{"number":"ACME-0001","month":"2026-10","total_cents":2000,"gross_usage_microcents":"1600000000","credit_used_microcents":"1600000000","lines":[{"kind":"usage","units":"400000000","amount_cents":1600,"gross_microcents":"1600000000","credit_microcents":"1600000000"},{"kind":"addon","description":"concurrency_pack","amount_cents":2000},{"kind":"credit","amount_cents":-1600}]}`},
		{name: "closing leaves nine dollars credit", method: "GET", path: "/customers/acme/credit", wantStatus: 200, wantBody: `{"credit_balance_microcents":"900000000","state_version":5,"pending_events":0,"processing_errors":0}`},
		{name: "credit and closing do not clear historical gross limit", method: "GET", path: "/customers/acme/months/2026-10/limit-status", wantStatus: 200, wantBody: `{"gross_charge_microcents":"1600000000","limit_cents":1500,"limit_reached":true,"state_version":5}`},
		{name: "raise limit to twenty dollars", method: "POST", path: "/customers/acme/spend-limit", inputBody: `{"idempotency_key":"covered-raised","limit_cents":2000}`, wantStatus: 200, wantBody: `{"limit_cents":2000}`},
		{name: "raised limit clears signal without changing usage", method: "GET", path: "/customers/acme/months/2026-10/limit-status", wantStatus: 200, wantBody: `{"gross_charge_microcents":"1600000000","limit_cents":2000,"limit_reached":false,"state_version":6,"pending_events":0,"processing_errors":0}`},
	}
	checks := []struct{ name, inputSQL, wantOutput string }{
		{name: "one grant one debit and nine dollars balance", inputSQL: `SELECT jsonb_build_array((SELECT count(*) FROM credit_entries WHERE amount_microcents>0),(SELECT count(*) FROM credit_entries WHERE amount_microcents<0),(SELECT credit_balance_microcents FROM customer_billing_state WHERE customer_id='acme'),(SELECT sum(amount_microcents) FROM credit_entries))::text`, wantOutput: `[1, 1, 900000000, 900000000]`},
		{name: "exact sixteen dollars gross", inputSQL: `SELECT jsonb_agg(gross_charge_microcents)::text FROM monthly_usage`, wantOutput: `[1600000000]`},
		{name: "one invoice and one rated receipt", inputSQL: `SELECT jsonb_build_array((SELECT count(*) FROM invoices),(SELECT count(*) FROM usage_ratings))::text`, wantOutput: `[1, 1]`},
	}
	e := newEnvironment(t, "covered-limit")
	e.migrate(t)
	e.start(t, "2026-10-31T00:00:00Z")
	if !e.runHTTPSteps(t, steps) {
		return
	}
	for _, check := range checks {
		e.assertSQL(t, check.name, check.inputSQL, check.wantOutput)
	}
	e.assertInvariants(t)
	e.save(t, "final-state.json", e.snapshot(t), false)
}
