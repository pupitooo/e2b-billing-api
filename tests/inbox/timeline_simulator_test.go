//go:build integration

package inbox_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"e2b/billing-api/internal/postgres"
	"e2b/billing-api/internal/simulator"
	"e2b/billing-api/internal/worker"
)

// billingTimelineScenario exposes each workflow and its literal storage totals.
type billingTimelineScenario struct {
	file                                                    string
	wantCompleted                                           int
	wantInbox, wantRatings, wantInvoices, wantCancellations int
	wantInvoiceCents                                        int64
	wantCredit                                              string
}

// testBillingTimeline declares the delayed platform workflow and literal storage totals.
func testBillingTimeline(t *testing.T) {
	runBillingTimeline(t, billingTimelineScenario{file: "billing-delayed-platform", wantCompleted: 35, wantInbox: 15, wantRatings: 15, wantInvoices: 5, wantCancellations: 1, wantInvoiceCents: 40_700, wantCredit: "0"})
}

// testMissedInvoiceTimeline declares the skipped manual invoice and recovery totals.
func testMissedInvoiceTimeline(t *testing.T) {
	runBillingTimeline(t, billingTimelineScenario{file: "billing-missed-invoice", wantCompleted: 27, wantInbox: 6, wantRatings: 6, wantInvoices: 4, wantCancellations: 0, wantInvoiceCents: 10_200, wantCredit: "0"})
}

// runBillingTimeline shares a trusted receipt clock across real API and worker
// calls, resumes the CLI after every step, and checks raw first-receipt timestamps.
func runBillingTimeline(t *testing.T, scenario billingTimelineScenario) {
	plan, err := simulator.LoadWorkflow("../../docs/simulator/" + scenario.file + ".json")
	if err != nil {
		t.Fatal(err)
	}
	pool := billingDatabase(t)
	var clock atomic.Value
	clock.Store(parseBillingTime(t, plan.Steps[0].BusinessTime))
	now := func() time.Time { return clock.Load().(time.Time) }
	store := newPostgresStore(t, pool, postgres.Config{Now: now})
	server := httptest.NewServer(newBillingHandlerWithClock(t, store, 5*time.Second, 8, now))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	w := worker.Worker{ProcessBatch: store.ProcessBatch, PollInterval: time.Millisecond, BatchTimeout: 5 * time.Second,
		HeartbeatFile: filepath.Join(t.TempDir(), "heartbeat"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("timeline worker shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("timeline worker did not stop before schema cleanup")
		}
	})
	binary := billingSimulatorBinary(t)
	state := filepath.Join(t.TempDir(), "timeline.json")
	arguments := append(billingSimulatorArguments(scenario.file, state, server.URL), "--mode=step")
	receipts := map[string]time.Time{}
	for index, step := range plan.Steps {
		t.Run(step.Name, func(t *testing.T) {
			clock.Store(parseBillingTime(t, step.BusinessTime))
			runBillingSimulator(t, binary, false, arguments...)
			assertWorkflowCheckpoint(t, state, index+1, "")
			if step.Request.Path != "/usage/batches" {
				return
			}
			var batch struct {
				Events []struct {
					EventID string `json:"event_id"`
				} `json:"events"`
			}
			if err := json.Unmarshal(step.Request.Body, &batch); err != nil {
				t.Fatal(err)
			}
			for _, event := range batch.Events {
				wanted, found := receipts[event.EventID]
				if !found {
					wanted = now()
					receipts[event.EventID] = wanted
				}
				var received time.Time
				if err := pool.QueryRow(ctx, "SELECT received_at FROM usage_inbox WHERE event_id=$1", event.EventID).Scan(&received); err != nil {
					t.Fatal(err)
				}
				if !received.Equal(wanted) {
					t.Fatalf("receipt %s at clock=%s received=%s; want original %s", event.EventID, step.BusinessTime, received, wanted)
				}
			}
		})
		if t.Failed() {
			return
		}
	}
	assertWorkflowCheckpoint(t, state, scenario.wantCompleted, "")
	var inbox, ratings, invoices, cancellations int
	var cents int64
	var credit string
	err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM usage_inbox),(SELECT count(*) FROM usage_ratings),
  (SELECT count(*) FROM invoices),(SELECT count(*) FROM addon_cancellations),(SELECT sum(total_cents) FROM invoices),
  credit_balance_microcents::text FROM customer_billing_state WHERE customer_id='acme'`).Scan(&inbox, &ratings, &invoices, &cancellations, &cents, &credit)
	if err != nil {
		t.Fatal(err)
	}
	if inbox != scenario.wantInbox || ratings != scenario.wantRatings || invoices != scenario.wantInvoices || cancellations != scenario.wantCancellations || cents != scenario.wantInvoiceCents || credit != scenario.wantCredit {
		t.Fatalf("timeline final inbox=%d ratings=%d invoices=%d cancellations=%d cents=%d credit=%s; want %+v", inbox, ratings, invoices, cancellations, cents, credit, scenario)
	}
}
