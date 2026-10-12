//go:build integration

package e2e_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// testUsageRecovery pairs new receipts with changed or invalid input, proving
// full batch rollback while the worker is paused. Concurrent identical retries
// preserve one receipt; a new worker then rates it once with literal gross.
func testUsageRecovery(t *testing.T) {
	scenario := struct {
		inputEvent, inputNewEvent                            string
		inputChangedUnits, inputInvalidUnits                 string
		inputClients                                         int
		wantAcceptedStatus                                   int
		wantPendingSQL, wantPending, wantFinalSQL, wantFinal string
	}{
		inputEvent:        `{"source":"e2e-usage","event_id":"original","schema_version":1,"customer_id":"acme","sandbox_id":"sandbox","metric":"cpu_seconds","period_start":"2026-10-10T12:00:00Z","period_end":"2026-10-10T13:00:00Z","units":100000000}`,
		inputNewEvent:     `{"source":"e2e-usage","event_id":"new-before-rejection","schema_version":1,"customer_id":"acme","sandbox_id":"sandbox","metric":"cpu_seconds","period_start":"2026-10-10T12:00:00Z","period_end":"2026-10-10T13:00:00Z","units":100000000}`,
		inputChangedUnits: `"units":100000001`, inputInvalidUnits: `"units":-1`, inputClients: 8, wantAcceptedStatus: 202,
		wantPendingSQL: `SELECT jsonb_build_array((SELECT count(*) FROM usage_inbox),(SELECT count(*) FROM usage_inbox WHERE processed_at IS NULL),(SELECT count(*) FROM usage_ratings),(SELECT count(*) FROM credit_entries))::text`, wantPending: `[1, 1, 0, 0]`,
		wantFinalSQL: `SELECT jsonb_build_array((SELECT count(*) FROM usage_inbox),(SELECT count(*) FROM usage_ratings),(SELECT sum(exact_charge_microcents) FROM rated_usage_groups),(SELECT sum(total_units) FROM rated_usage_groups),(SELECT count(*) FROM credit_entries))::text`, wantFinal: `[1, 1, 400000000, 100000000, 0]`,
	}
	steps := []httpStep{
		{name: "commit original receipt", method: "POST", path: "/usage/batches", inputBody: `{"events":[` + scenario.inputEvent + `]}`, wantStatus: scenario.wantAcceptedStatus, wantBody: `{"status":"accepted"}`},
		{name: "changed receipt rolls back preceding new receipt", method: "POST", path: "/usage/batches", inputBody: `{"events":[` + scenario.inputNewEvent + `,` + strings.Replace(scenario.inputEvent, `"units":100000000`, scenario.inputChangedUnits, 1) + `]}`, wantStatus: 409, wantBody: `{"error":{"code":"event_conflict","source":"e2e-usage","event_id":"original"}}`, wantUnchangedState: true},
		{name: "invalid receipt rolls back preceding new receipt", method: "POST", path: "/usage/batches", inputBody: `{"events":[` + scenario.inputNewEvent + `,` + strings.Replace(scenario.inputEvent, `"units":100000000`, scenario.inputInvalidUnits, 1) + `]}`, wantStatus: 422, wantBody: `{"error":{"code":"invalid_batch","field":"events[1].units"}}`, wantUnchangedState: true},
	}
	e := newEnvironment(t, "usage-recovery")
	e.migrate(t)
	e.start(t, "2026-10-31T00:00:00Z")
	e.compose(t, "stop", "worker")
	if !e.runHTTPSteps(t, steps) {
		return
	}
	before := e.snapshot(t)
	var clients sync.WaitGroup
	start := make(chan struct{})
	for client := range scenario.inputClients {
		clients.Go(func() {
			<-start
			// Each client owns its test assertions and shares only immutable inputs.
			t.Run(fmt.Sprintf("concurrent unchanged retry %d", client), func(t *testing.T) {
				e.request(t, httpStep{name: "concurrent retry", method: "POST", path: "/usage/batches", inputBody: `{"events":[` + scenario.inputEvent + `]}`, wantStatus: scenario.wantAcceptedStatus})
			})
		})
	}
	close(start)
	clients.Wait()
	if t.Failed() {
		return
	}
	if after := e.snapshot(t); after != before {
		t.Fatal("Concurrent unchanged retries changed original receipt or financial state")
	}
	e.assertSQL(t, "one pending receipt before worker restart", scenario.wantPendingSQL, scenario.wantPending)
	e.compose(t, "up", "-d", "--no-deps", "--wait", "--wait-timeout", "90", "worker")
	e.awaitSQL(t, "restarted worker accounts original once", `SELECT count(*)::text FROM usage_inbox WHERE processed_at IS NULL`, "0")
	e.assertSQL(t, "one rating and exact four dollar gross", scenario.wantFinalSQL, scenario.wantFinal)
	e.assertInvariants(t)
}
