//go:build integration

package inbox_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"e2b/billing-api/internal/accounting"
	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/usage"
)

type missingPriceLog struct {
	Level       string `json:"level"`
	Priority    string `json:"priority"`
	ErrorCode   string `json:"error_code"`
	Source      string `json:"source"`
	EventID     string `json:"event_id"`
	CustomerID  string `json:"customer_id"`
	Metric      string `json:"metric"`
	PeriodStart string `json:"period_start"`
}

// testMissingPriceProcessing verifies P0 quarantine and explicit recovery in
// private catalogs. Missing, future-only, and other-customer prices cannot charge
// usage; an operator supplies a dated price and releases the unchanged receipt.
func testMissingPriceProcessing(t *testing.T) {
	t.Helper()

	cases := []struct {
		name                   string
		catalogSQL             string
		metric                 string
		periodStart            string
		periodEnd              string
		receivedAt             string
		units                  int64
		initialCreditTicks     string
		recoveryEffective      string
		recoveryPriceCents     int64
		wantError              error
		wantQuarantineWork     bool
		wantProcessed          bool
		wantIdleWork           bool
		wantRecoveredWork      bool
		wantRecoveredProcessed bool
		wantRecoveredError     string
		wantProcessingError    string
		wantLog                missingPriceLog
		wantQuarantineState    priceBoundaryState
		wantRecoveredState     priceBoundaryState
	}{
		{
			name:       "new metric without a provisioned price is a P0 incident",
			catalogSQL: "INSERT INTO metrics VALUES ('memory_seconds')",
			metric:     "memory_seconds", periodStart: "2026-10-10T12:00:00Z", periodEnd: "2026-10-10T13:00:00Z",
			receivedAt: "2026-10-11T00:00:00Z", units: 100_000_000, initialCreditTicks: "500000000",
			recoveryEffective: "2026-10-01T00:00:00Z", recoveryPriceCents: 4,
			wantError: nil, wantQuarantineWork: true, wantProcessed: false, wantIdleWork: false,
			wantRecoveredWork: true, wantRecoveredProcessed: true, wantRecoveredError: "",
			wantProcessingError: "P0 missing_valid_price: no valid price for customer acme and metric memory_seconds at 2026-10-10T12:00:00Z",
			wantLog:             missingPriceLog{Level: "ERROR", Priority: "P0", ErrorCode: "missing_valid_price", Source: "catalog-test", EventID: "one", CustomerID: "acme", Metric: "memory_seconds", PeriodStart: "2026-10-10T12:00:00Z"},
			wantQuarantineState: priceBoundaryState{Units: "0", GrossTicks: "0", AllocatedCreditTicks: "0", MonthlyGrossTicks: "0", CreditDebitTicks: "0", CreditBalanceTicks: "500000000"},
			wantRecoveredState: priceBoundaryState{
				Groups: 1, Units: "100000000", PriceVersionID: "catalog-recovery", UsageMonth: "2026-10-01", BillingMonth: "2026-10-01",
				GrossTicks: "400000000", AllocatedCreditTicks: "400000000", BookedCents: 400,
				Ratings: 1, MonthlyRows: 1, MonthlyGrossTicks: "400000000", CreditDebits: 1, CreditDebitTicks: "-400000000", CreditBalanceTicks: "100000000", StateVersion: 1,
			},
		},
		{
			name:   "usage before the seed's first effective instant is a P0 incident",
			metric: "cpu_seconds", periodStart: "2026-09-30T12:00:00Z", periodEnd: "2026-09-30T13:00:00Z",
			receivedAt: "2026-10-01T00:00:00Z", units: 100_000_000, initialCreditTicks: "500000000",
			recoveryEffective: "2026-09-01T00:00:00Z", recoveryPriceCents: 5,
			wantError: nil, wantQuarantineWork: true, wantProcessed: false, wantIdleWork: false,
			wantRecoveredWork: true, wantRecoveredProcessed: true, wantRecoveredError: "",
			wantProcessingError: "P0 missing_valid_price: no valid price for customer acme and metric cpu_seconds at 2026-09-30T12:00:00Z",
			wantLog:             missingPriceLog{Level: "ERROR", Priority: "P0", ErrorCode: "missing_valid_price", Source: "catalog-test", EventID: "one", CustomerID: "acme", Metric: "cpu_seconds", PeriodStart: "2026-09-30T12:00:00Z"},
			wantQuarantineState: priceBoundaryState{Units: "0", GrossTicks: "0", AllocatedCreditTicks: "0", MonthlyGrossTicks: "0", CreditDebitTicks: "0", CreditBalanceTicks: "500000000"},
			wantRecoveredState: priceBoundaryState{
				Groups: 1, Units: "100000000", PriceVersionID: "catalog-recovery", UsageMonth: "2026-09-01", BillingMonth: "2026-09-01",
				GrossTicks: "500000000", AllocatedCreditTicks: "500000000", BookedCents: 500,
				Ratings: 1, MonthlyRows: 1, MonthlyGrossTicks: "500000000", CreditDebits: 1, CreditDebitTicks: "-500000000", CreditBalanceTicks: "0", StateVersion: 1,
			},
		},
		{
			name: "a future price cannot cover earlier consumption",
			catalogSQL: `INSERT INTO metrics VALUES ('memory_seconds');
				INSERT INTO price_versions VALUES ('future-price',NULL,'memory_seconds',6,'2026-11-01T00:00:00Z')`,
			metric: "memory_seconds", periodStart: "2026-10-10T12:00:00Z", periodEnd: "2026-10-10T13:00:00Z",
			receivedAt: "2026-10-11T00:00:00Z", units: 100_000_000, initialCreditTicks: "500000000",
			recoveryEffective: "2026-10-01T00:00:00Z", recoveryPriceCents: 4,
			wantError: nil, wantQuarantineWork: true, wantProcessed: false, wantIdleWork: false,
			wantRecoveredWork: true, wantRecoveredProcessed: true, wantRecoveredError: "",
			wantProcessingError: "P0 missing_valid_price: no valid price for customer acme and metric memory_seconds at 2026-10-10T12:00:00Z",
			wantLog:             missingPriceLog{Level: "ERROR", Priority: "P0", ErrorCode: "missing_valid_price", Source: "catalog-test", EventID: "one", CustomerID: "acme", Metric: "memory_seconds", PeriodStart: "2026-10-10T12:00:00Z"},
			wantQuarantineState: priceBoundaryState{Units: "0", GrossTicks: "0", AllocatedCreditTicks: "0", MonthlyGrossTicks: "0", CreditDebitTicks: "0", CreditBalanceTicks: "500000000"},
			wantRecoveredState: priceBoundaryState{
				Groups: 1, Units: "100000000", PriceVersionID: "catalog-recovery", UsageMonth: "2026-10-01", BillingMonth: "2026-10-01",
				GrossTicks: "400000000", AllocatedCreditTicks: "400000000", BookedCents: 400,
				Ratings: 1, MonthlyRows: 1, MonthlyGrossTicks: "400000000", CreditDebits: 1, CreditDebitTicks: "-400000000", CreditBalanceTicks: "100000000", StateVersion: 1,
			},
		},
		{
			name: "another customer's price cannot replace the required default",
			catalogSQL: `INSERT INTO metrics VALUES ('memory_seconds');
				INSERT INTO price_versions VALUES ('other-customer-price','cyberdyne','memory_seconds',4,'2026-10-01T00:00:00Z')`,
			metric: "memory_seconds", periodStart: "2026-10-10T12:00:00Z", periodEnd: "2026-10-10T13:00:00Z",
			receivedAt: "2026-10-11T00:00:00Z", units: 100_000_000, initialCreditTicks: "500000000",
			recoveryEffective: "2026-10-01T00:00:00Z", recoveryPriceCents: 4,
			wantError: nil, wantQuarantineWork: true, wantProcessed: false, wantIdleWork: false,
			wantRecoveredWork: true, wantRecoveredProcessed: true, wantRecoveredError: "",
			wantProcessingError: "P0 missing_valid_price: no valid price for customer acme and metric memory_seconds at 2026-10-10T12:00:00Z",
			wantLog:             missingPriceLog{Level: "ERROR", Priority: "P0", ErrorCode: "missing_valid_price", Source: "catalog-test", EventID: "one", CustomerID: "acme", Metric: "memory_seconds", PeriodStart: "2026-10-10T12:00:00Z"},
			wantQuarantineState: priceBoundaryState{Units: "0", GrossTicks: "0", AllocatedCreditTicks: "0", MonthlyGrossTicks: "0", CreditDebitTicks: "0", CreditBalanceTicks: "500000000"},
			wantRecoveredState: priceBoundaryState{
				Groups: 1, Units: "100000000", PriceVersionID: "catalog-recovery", UsageMonth: "2026-10-01", BillingMonth: "2026-10-01",
				GrossTicks: "400000000", AllocatedCreditTicks: "400000000", BookedCents: 400,
				Ratings: 1, MonthlyRows: 1, MonthlyGrossTicks: "400000000", CreditDebits: 1, CreditDebitTicks: "-400000000", CreditBalanceTicks: "100000000", StateVersion: 1,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			logs := captureBillingLogs(t)
			if tc.catalogSQL != "" {
				if _, err := pool.Exec(ctx, tc.catalogSQL); err != nil {
					t.Fatalf("Prepare catalog %q: %v", tc.catalogSQL, err)
				}
			}
			if _, err := pool.Exec(ctx, "UPDATE customer_billing_state SET credit_balance_ticks=$1::numeric WHERE customer_id='acme'", tc.initialCreditTicks); err != nil {
				t.Fatalf("Prepare credit %s: %v", tc.initialCreditTicks, err)
			}
			event := usage.Event{Source: "catalog-test", EventID: "one", SchemaVersion: 1, CustomerID: "acme", SandboxID: "sandbox", Metric: tc.metric,
				PeriodStart: parseBillingTime(t, tc.periodStart), PeriodEnd: parseBillingTime(t, tc.periodEnd), Units: tc.units}
			if err := inbox.NewPostgres(pool, time.Second).InsertBatch(ctx, []usage.Event{event}, parseBillingTime(t, tc.receivedAt)); err != nil {
				t.Fatalf("Insert missing-price receipt %+v: %v", event, err)
			}

			store := billing.NewStore(pool)
			worked, err := store.ProcessBatch(ctx)
			if err != tc.wantError {
				t.Fatalf("ProcessBatch(%+v) quarantine error=%v; want %v", event, err, tc.wantError)
			}
			if worked != tc.wantQuarantineWork {
				t.Errorf("ProcessBatch(%+v) worked=%t; want %t after committed quarantine", event, worked, tc.wantQuarantineWork)
			}

			var processed bool
			var processingError string
			if err := pool.QueryRow(ctx, "SELECT processed_at IS NOT NULL,COALESCE(processing_error,'') FROM usage_inbox WHERE source=$1 AND event_id=$2", event.Source, event.EventID).Scan(&processed, &processingError); err != nil {
				t.Fatalf("Read missing-price receipt: %v", err)
			}
			if processed != tc.wantProcessed || processingError != tc.wantProcessingError {
				t.Errorf("Missing-price receipt processed=%t error=%q; want %t %q", processed, processingError, tc.wantProcessed, tc.wantProcessingError)
			}
			state, err := readPriceBoundaryState(ctx, pool, event.CustomerID)
			if err != nil {
				t.Fatalf("Read quarantined financial state: %v", err)
			}
			if state != tc.wantQuarantineState {
				t.Errorf("Missing-price financial state=%+v; want %+v", state, tc.wantQuarantineState)
			}
			if got := readMissingPriceLogs(t, logs); !reflect.DeepEqual(got, []missingPriceLog{tc.wantLog}) {
				t.Errorf("Missing-price log=%+v; want one report %+v", got, tc.wantLog)
			}

			worked, err = store.ProcessBatch(ctx)
			if err != tc.wantError {
				t.Fatalf("ProcessBatch with quarantined receipt error=%v; want %v", err, tc.wantError)
			}
			if worked != tc.wantIdleWork {
				t.Errorf("ProcessBatch with quarantined receipt worked=%t; want %t", worked, tc.wantIdleWork)
			}
			price := accounting.PriceVersion{ID: "catalog-recovery", Metric: tc.metric, EffectiveFrom: parseBillingTime(t, tc.recoveryEffective), PricePerMillionCents: tc.recoveryPriceCents}
			if _, err := pool.Exec(ctx, "INSERT INTO price_versions VALUES ($1,NULL,$2,$3,$4)", price.ID, price.Metric, price.PricePerMillionCents, price.EffectiveFrom); err != nil {
				t.Fatalf("Create recovery price %+v: %v", price, err)
			}
			worked, err = store.ProcessBatch(ctx)
			if err != tc.wantError {
				t.Fatalf("ProcessBatch before explicit release error=%v; want %v", err, tc.wantError)
			}
			if worked != tc.wantIdleWork {
				t.Errorf("ProcessBatch before explicit release worked=%t; want %t", worked, tc.wantIdleWork)
			}
			if _, err := pool.Exec(ctx, "UPDATE usage_inbox SET processing_error=NULL WHERE source=$1 AND event_id=$2 AND processed_at IS NULL AND processing_error=$3", event.Source, event.EventID, tc.wantProcessingError); err != nil {
				t.Fatalf("Release investigated missing-price receipt: %v", err)
			}

			worked, err = store.ProcessBatch(ctx)
			if err != tc.wantError {
				t.Fatalf("ProcessBatch after catalog recovery error=%v; want %v", err, tc.wantError)
			}
			if worked != tc.wantRecoveredWork {
				t.Errorf("ProcessBatch after catalog recovery worked=%t; want %t", worked, tc.wantRecoveredWork)
			}

			if err := pool.QueryRow(ctx, "SELECT processed_at IS NOT NULL,COALESCE(processing_error,'') FROM usage_inbox WHERE source=$1 AND event_id=$2", event.Source, event.EventID).Scan(&processed, &processingError); err != nil {
				t.Fatalf("Read recovered receipt: %v", err)
			}
			if processed != tc.wantRecoveredProcessed || processingError != tc.wantRecoveredError {
				t.Errorf("Recovered receipt processed=%t error=%q; want %t %q", processed, processingError, tc.wantRecoveredProcessed, tc.wantRecoveredError)
			}
			state, err = readPriceBoundaryState(ctx, pool, event.CustomerID)
			if err != nil {
				t.Fatalf("Read recovered financial state: %v", err)
			}
			if state != tc.wantRecoveredState {
				t.Errorf("Recovered financial state=%+v; want %+v", state, tc.wantRecoveredState)
			}
			worked, err = store.ProcessBatch(ctx)
			if err != tc.wantError {
				t.Fatalf("ProcessBatch after recovered completion error=%v; want %v", err, tc.wantError)
			}
			if worked != tc.wantIdleWork {
				t.Errorf("ProcessBatch after recovered completion worked=%t; want %t", worked, tc.wantIdleWork)
			}
			if got := readMissingPriceLogs(t, logs); !reflect.DeepEqual(got, []missingPriceLog{tc.wantLog}) {
				t.Errorf("Missing-price reports after recovery=%+v; want only original %+v", got, tc.wantLog)
			}
		})
	}
	t.Run("failed quarantine commit retains pending usage without a committed P0 report", func(t *testing.T) {
		scenario := struct {
			catalogSQL          string
			metric              string
			periodStart         string
			periodEnd           string
			units               int64
			wantError           bool
			wantSQLState        string
			wantWork            bool
			wantProcessed       bool
			wantProcessingError string
			wantLogCount        int
			wantState           priceBoundaryState
		}{
			catalogSQL: `INSERT INTO metrics VALUES ('memory_seconds');
				-- Reject quarantine at commit to verify the post-commit report boundary.
				CREATE FUNCTION fail_price_quarantine() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN RAISE EXCEPTION 'fixture quarantine commit failure' USING ERRCODE='23514'; END; $$;
				CREATE CONSTRAINT TRIGGER fail_price_quarantine AFTER UPDATE ON usage_inbox
				DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_price_quarantine()`,
			metric: "memory_seconds", periodStart: "2026-10-10T12:00:00Z", periodEnd: "2026-10-10T13:00:00Z", units: 100_000_000,
			wantError: true, wantSQLState: "23514", wantWork: false, wantProcessed: false, wantProcessingError: "", wantLogCount: 0,
			wantState: priceBoundaryState{Units: "0", GrossTicks: "0", AllocatedCreditTicks: "0", MonthlyGrossTicks: "0", CreditDebitTicks: "0", CreditBalanceTicks: "0"},
		}
		pool := billingDatabase(t)
		ctx := context.Background()
		logs := captureBillingLogs(t)
		if _, err := pool.Exec(ctx, scenario.catalogSQL); err != nil {
			t.Fatalf("Prepare deferred quarantine failure: %v", err)
		}
		event := usage.Event{Source: "catalog-test", EventID: "one", SchemaVersion: 1, CustomerID: "acme", SandboxID: "sandbox", Metric: scenario.metric,
			PeriodStart: parseBillingTime(t, scenario.periodStart), PeriodEnd: parseBillingTime(t, scenario.periodEnd), Units: scenario.units}
		if err := inbox.NewPostgres(pool, time.Second).InsertBatch(ctx, []usage.Event{event}, event.PeriodEnd); err != nil {
			t.Fatalf("Insert receipt before quarantine failure: %v", err)
		}

		worked, err := billing.NewStore(pool).ProcessBatch(ctx)
		if (err != nil) != scenario.wantError {
			t.Fatalf("ProcessBatch quarantine commit error=%v; wantError=%t", err, scenario.wantError)
		}
		assertPostgresError(t, err, scenario.wantSQLState)
		if worked != scenario.wantWork {
			t.Errorf("ProcessBatch failed quarantine commit worked=%t; want %t", worked, scenario.wantWork)
		}

		var processed bool
		var processingError string
		if err := pool.QueryRow(ctx, "SELECT processed_at IS NOT NULL,COALESCE(processing_error,'') FROM usage_inbox").Scan(&processed, &processingError); err != nil {
			t.Fatalf("Read rolled-back quarantine: %v", err)
		}
		if processed != scenario.wantProcessed || processingError != scenario.wantProcessingError {
			t.Errorf("Failed quarantine receipt processed=%t error=%q; want %t %q", processed, processingError, scenario.wantProcessed, scenario.wantProcessingError)
		}
		state, err := readPriceBoundaryState(ctx, pool, event.CustomerID)
		if err != nil {
			t.Fatalf("Read financial state after failed quarantine commit: %v", err)
		}
		if state != scenario.wantState {
			t.Errorf("Failed quarantine financial state=%+v; want %+v", state, scenario.wantState)
		}
		if got := len(readMissingPriceLogs(t, logs)); got != scenario.wantLogCount {
			t.Errorf("Failed quarantine committed report count=%d; want %d", got, scenario.wantLogCount)
		}
	})
}

// captureBillingLogs isolates the default logger for a serial billing scenario
// and restores it before the next scenario or background worker can use it.
func captureBillingLogs(t testing.TB) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &output
}

// readMissingPriceLogs decodes observable report fields without deriving any
// expected classification or receipt context from production accounting code.
func readMissingPriceLogs(t testing.TB, output *bytes.Buffer) []missingPriceLog {
	t.Helper()
	var reports []missingPriceLog
	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var report missingPriceLog
		if err := json.Unmarshal(line, &report); err != nil {
			t.Fatalf("Decode billing report %q: %v", line, err)
		}
		reports = append(reports, report)
	}
	return reports
}
