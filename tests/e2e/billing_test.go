//go:build integration

package e2e_test

import "testing"

type workflowScenario struct {
	name, file, initialTime string
	clockChanges            map[string]string
	wantSteps               int
}

// TestBillingE2E covers the backend acceptance procedure and every reviewed
// public JSON workflow against separate API, worker and PostgreSQL containers.
// Each scenario resets only its owned database and declares its calendar inputs;
// literal request/response expectations remain alongside the steps in JSON.
func TestBillingE2E(t *testing.T) {
	t.Run("fresh and concurrent migrations preserve seeds", testMigrations)
	t.Run("assignment survives API outage worker pause and service restart", testAssignment)
	t.Run("usage conflicts and concurrent retries recover once", testUsageRecovery)
	t.Run("credit covered gross limit excludes add-on", testCoveredLimit)
	t.Run("six month clock jump catches up recurring invoices", testHalfYear)
	scenarios := []workflowScenario{
		{name: "fixed clock invoice", file: "billing-demo-invoice", initialTime: "2026-11-01T00:00:00Z", wantSteps: 9},
		{name: "command retries and conflicts", file: "billing-command-retries", initialTime: "2026-12-01T00:00:00Z", wantSteps: 16},
		{name: "exact subcent credit", file: "billing-exact-credit", initialTime: "2026-12-01T00:00:00Z", wantSteps: 6},
		{name: "zero cent invoices without remainder carry", file: "billing-subcent-invoices", initialTime: "2027-01-01T00:00:00Z", wantSteps: 15},
		{name: "credit exhaustion preserves full add-on fee", file: "billing-credit-exhaustion", initialTime: "2026-12-01T00:00:00Z", wantSteps: 9},
		{name: "every recurring add-on month is retained", file: "billing-addon-order", initialTime: "2027-01-01T00:00:00Z", wantSteps: 14},
		{name: "monthly limit configuration and reset", file: "billing-limit-status", initialTime: "2026-12-01T00:00:00Z", wantSteps: 15},
		{name: "historical prices and customer override", file: "billing-price-versions", initialTime: "2026-10-01T00:00:00Z", clockChanges: map[string]string{"measure at both effective consumption prices": "2026-11-01T00:00:00Z", "reject a new default that changes rated history": "2026-12-01T00:00:00Z"}, wantSteps: 8},
		{name: "price boundary quarantine permits closing", file: "billing-price-boundary", initialTime: "2026-12-01T00:00:00Z", wantSteps: 5},
		{name: "UTC month boundary quarantine permits closing", file: "billing-month-boundary", initialTime: "2026-12-01T00:00:00Z", wantSteps: 5},
		{name: "multi month delayed platform billing", file: "billing-delayed-platform", initialTime: "2026-09-01T00:00:00Z", wantSteps: 35},
		{name: "missed manual invoices recover atomically", file: "billing-missed-invoice", initialTime: "2026-10-31T00:00:00Z", wantSteps: 27},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) { runWorkflow(t, scenario) })
	}
}
