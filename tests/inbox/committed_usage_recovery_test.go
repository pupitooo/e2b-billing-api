//go:build integration

package inbox_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testCommittedUsageRecovery loses a real HTTP reply or kills the CLI only after
// the database has committed its receipt. No sleep decides the crash point. A
// new pool/store and new producer resume the old checkpoint and rate exactly once.
func testCommittedUsageRecovery(t *testing.T) {
	cases := []struct {
		name                                                        string
		killProducer                                                bool
		grantCents                                                  int64
		usageBody, serverTime                                       string
		wantFirstError                                              bool
		wantFirstExitCode, wantFirstCheckpoint, wantFinalCheckpoint int
		wantFirstOutput                                             string
		wantState                                                   resourceState
		wantReceipts, wantLedgerEntries                             int
		wantOriginalReceipt                                         string
	}{
		{name: "committed HTTP response is lost", grantCents: 1, usageBody: `{"events":[{"source":"recovery-producer","event_id":"one","schema_version":1,"customer_id":"acme","sandbox_id":"sandbox","metric":"cpu_seconds","period_start":"2026-10-10T12:00:00Z","period_end":"2026-10-10T13:00:00Z","units":125000}]}`, serverTime: "2026-11-01T00:00:00Z", wantFirstError: true, wantFirstExitCode: 1, wantFirstCheckpoint: 0, wantFinalCheckpoint: 2, wantFirstOutput: "EOF", wantState: resourceState{prices: 3, operations: 1, ratings: 1, stateVersion: 2, creditMicrocents: "500000"}, wantReceipts: 1, wantLedgerEntries: 2, wantOriginalReceipt: "2026-11-01T00:00:00Z"},
		{name: "producer killed after commit before checkpoint", killProducer: true, grantCents: 1, usageBody: `{"events":[{"source":"recovery-producer","event_id":"one","schema_version":1,"customer_id":"acme","sandbox_id":"sandbox","metric":"cpu_seconds","period_start":"2026-10-10T12:00:00Z","period_end":"2026-10-10T13:00:00Z","units":125000}]}`, serverTime: "2026-11-01T00:00:00Z", wantFirstError: true, wantFirstExitCode: -1, wantFirstCheckpoint: 0, wantFinalCheckpoint: 2, wantState: resourceState{prices: 3, operations: 1, ratings: 1, stateVersion: 2, creditMicrocents: "500000"}, wantReceipts: 1, wantLedgerEntries: 2, wantOriginalReceipt: "2026-11-01T00:00:00Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			clock := parseBillingTime(t, tc.serverTime)
			store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return clock }})
			if err := store.GrantCredit(ctx, "acme", billing.CreditGrant{IdempotencyKey: "upfront", AmountCents: tc.grantCents}); err != nil {
				t.Fatal(err)
			}
			handler := newBillingHandlerWithClock(t, store, 5*time.Second, 8, func() time.Time { return clock })
			type commitResult struct {
				status int
				body   string
			}
			committed := make(chan commitResult, 1)
			requestBodies := make(chan []byte, 2)
			release := make(chan struct{})
			defer close(release)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/usage/batches" {
					handler.ServeHTTP(w, r)
					return
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
				requestBodies <- body
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, r)
				if requests.Add(1) == 1 {
					committed <- commitResult{status: recorder.Code, body: recorder.Body.String()}
					if tc.killProducer {
						select {
						case <-r.Context().Done():
						case <-release:
						}
					} else {
						connection, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						if err := connection.Close(); err != nil {
							t.Error(err)
						}
					}
					return
				}
				for key, values := range recorder.Header() {
					for _, value := range values {
						w.Header().Add(key, value)
					}
				}
				w.WriteHeader(recorder.Code)
				if _, err := w.Write(recorder.Body.Bytes()); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(server.Close)
			directory := t.TempDir()
			statePath, planPath := filepath.Join(directory, "state.json"), filepath.Join(directory, "plan.json")
			plan := `{"name":"committed-receipt-recovery","steps":[{"name":"accept original measurement","request":{"method":"POST","path":"/usage/batches","body":` + tc.usageBody + `},"want":{"status":202,"body":{"status":"accepted"}}},{"name":"read exactly one credit allocation","request":{"method":"GET","path":"/customers/acme/credit"},"want":{"status":200,"body":{"customer_id":"acme","credit_balance_microcents":"500000","state_version":2,"pending_events":0,"processing_errors":0}}}]}`
			if err := os.WriteFile(planPath, []byte(plan), 0600); err != nil {
				t.Fatal(err)
			}
			binary := billingSimulatorBinary(t)
			arguments := []string{"--scenario=billing-recovery", "--file=" + planPath, "--state=" + statePath, "--source=recovery-producer", "--api-url=" + server.URL, "--max-attempts=1", "--timeout=20s"}
			command := exec.CommandContext(ctx, binary, arguments...)
			var output bytes.Buffer
			command.Stdout, command.Stderr = &output, &output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			defer command.Process.Kill()

			select {
			case result := <-committed:
				if result.status != 202 {
					t.Fatalf("Initial committed receipt: HTTP=%d body=%s; want 202", result.status, result.body)
				}
			case <-ctx.Done():
				t.Fatalf("Await committed receipt: %v", ctx.Err())
			}
			if tc.killProducer {
				assertWorkflowCheckpoint(t, statePath, tc.wantFirstCheckpoint, "")
				if err := command.Process.Kill(); err != nil {
					t.Fatal(err)
				}
			}
			var firstError error
			select {
			case firstError = <-done:
			case <-ctx.Done():
				t.Fatalf("Await first producer exit: %v", ctx.Err())
			}

			if (firstError != nil) != tc.wantFirstError {
				t.Fatalf("First producer: error=%v; wantError=%t output=%s", firstError, tc.wantFirstError, output.String())
			}
			var exit *exec.ExitError
			if !errors.As(firstError, &exit) || exit.ExitCode() != tc.wantFirstExitCode {
				t.Fatalf("First producer: error=%v; want exit=%d output=%s", firstError, tc.wantFirstExitCode, output.String())
			}
			assertWorkflowCheckpoint(t, statePath, tc.wantFirstCheckpoint, tc.wantFirstOutput)
			var receipt time.Time
			var receipts, ratings int
			if err := pool.QueryRow(ctx, `SELECT received_at,(SELECT count(*) FROM usage_inbox),(SELECT count(*) FROM usage_ratings) FROM usage_inbox WHERE source='recovery-producer' AND event_id='one'`).Scan(&receipt, &receipts, &ratings); err != nil {
				t.Fatal(err)
			}
			if receipts != tc.wantReceipts || ratings != 0 || !receipt.Equal(parseBillingTime(t, tc.wantOriginalReceipt)) {
				t.Fatalf("Committed receipt before restart: count=%d ratings=%d received=%s; want %d 0 %s", receipts, ratings, receipt, tc.wantReceipts, tc.wantOriginalReceipt)
			}

			restartedPool, err := pgxpool.NewWithConfig(ctx, pool.Config())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(restartedPool.Close)
			restarted := newPostgresStore(t, restartedPool, postgres.Config{Now: func() time.Time { return clock.Add(time.Hour) }})
			processUsageSteps(t, restarted, []bool{true, false})
			beforeRetry := addonFinancialSnapshot(t, restartedPool)
			resumedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path == "/usage/batches" {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
					requestBodies <- body
					r.Body = io.NopCloser(bytes.NewReader(body))
					requests.Add(1)
				}
				newBillingHandlerWithClock(t, restarted, 5*time.Second, 8, func() time.Time { return clock.Add(time.Hour) }).ServeHTTP(w, r)
			}))
			defer resumedServer.Close()
			arguments[4] = "--api-url=" + resumedServer.URL

			runBillingSimulator(t, binary, false, arguments...)

			assertWorkflowCheckpoint(t, statePath, tc.wantFinalCheckpoint, "")
			if after := addonFinancialSnapshot(t, restartedPool); after != beforeRetry {
				t.Fatalf("Resumed producer changed committed financial rows: before=%s after=%s", beforeRetry, after)
			}
			if requests.Load() != 2 {
				t.Fatalf("Usage requests=%d; want 2", requests.Load())
			}
			originalBody, retryBody := <-requestBodies, <-requestBodies
			if !bytes.Equal(originalBody, retryBody) {
				t.Fatalf("Usage retry body=%s; want original %s", retryBody, originalBody)
			}
			assertResourceState(t, restartedPool, tc.wantState)
			assertAccountingInvariants(t, restartedPool)
			var entries int
			if err := restartedPool.QueryRow(ctx, "SELECT count(*) FROM credit_entries").Scan(&entries); err != nil {
				t.Fatal(err)
			}
			if entries != tc.wantLedgerEntries {
				t.Fatalf("Recovered ledger entries=%d; want %d", entries, tc.wantLedgerEntries)
			}
		})
	}
}
