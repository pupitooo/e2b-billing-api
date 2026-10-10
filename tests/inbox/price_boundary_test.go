//go:build integration

package inbox_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/usage"
	"github.com/jackc/pgx/v5/pgxpool"
)

type priceBoundaryState struct {
	Groups               int
	Units                string
	PriceVersionID       string
	UsageMonth           string
	BillingMonth         string
	GrossTicks           string
	AllocatedCreditTicks string
	BookedCents          int64
	Ratings              int
	MonthlyRows          int
	MonthlyGrossTicks    string
	CreditDebits         int
	CreditDebitTicks     string
	CreditBalanceTicks   string
	StateVersion         int64
}

// testPriceBoundaryProcessing adds isolated ProcessBatch scenarios for the exact
// start/end of a price change and the first unsupported microsecond beyond it.
// Literal expectations cover receipt completion, quarantine, retry exclusion,
// historical group identity, and every financial projection touched by rating.
func testPriceBoundaryProcessing(t *testing.T) {
	t.Helper()

	cases := []struct {
		name                string
		customerID          string
		periodStart         string
		periodEnd           string
		receivedAt          string
		units               int64
		initialCreditTicks  string
		wantError           error
		wantWork            bool
		wantProcessed       bool
		wantProcessingError string
		wantNextWork        bool
		wantState           priceBoundaryState
	}{
		{
			name:               "price change at excluded end preserves historical rating",
			customerID:         "cyberdyne",
			periodStart:        "2026-10-14T23:59:00Z",
			periodEnd:          "2026-10-15T00:00:00Z",
			receivedAt:         "2026-11-01T00:00:00Z",
			units:              1_000_000,
			initialCreditTicks: "2000000",
			wantError:          nil,
			wantWork:           true,
			wantProcessed:      true,
			wantNextWork:       false,
			wantState: priceBoundaryState{
				Groups: 1, Units: "1000000", PriceVersionID: "cpu-default-2026-10-01",
				UsageMonth: "2026-10-01", BillingMonth: "2026-10-01",
				GrossTicks: "5000000", AllocatedCreditTicks: "2000000", BookedCents: 5,
				Ratings: 1, MonthlyRows: 1, MonthlyGrossTicks: "5000000",
				CreditDebits: 1, CreditDebitTicks: "-2000000", CreditBalanceTicks: "0", StateVersion: 1,
			},
		},
		{
			name:               "price change at included start selects new rating",
			customerID:         "cyberdyne",
			periodStart:        "2026-10-15T00:00:00Z",
			periodEnd:          "2026-10-15T00:01:00Z",
			receivedAt:         "2026-11-01T00:00:00Z",
			units:              1_000_000,
			initialCreditTicks: "2000000",
			wantError:          nil,
			wantWork:           true,
			wantProcessed:      true,
			wantNextWork:       false,
			wantState: priceBoundaryState{
				Groups: 1, Units: "1000000", PriceVersionID: "cpu-default-2026-10-15",
				UsageMonth: "2026-10-01", BillingMonth: "2026-10-01",
				GrossTicks: "6000000", AllocatedCreditTicks: "2000000", BookedCents: 6,
				Ratings: 1, MonthlyRows: 1, MonthlyGrossTicks: "6000000",
				CreditDebits: 1, CreditDebitTicks: "-2000000", CreditBalanceTicks: "0", StateVersion: 1,
			},
		},
		{
			name:                "one microsecond across price change quarantines without financial effects",
			customerID:          "cyberdyne",
			periodStart:         "2026-10-14T23:59:00Z",
			periodEnd:           "2026-10-15T00:00:00.000001Z",
			receivedAt:          "2026-11-01T00:00:00Z",
			units:               1_000_000,
			initialCreditTicks:  "2000000",
			wantError:           nil,
			wantWork:            true,
			wantProcessed:       false,
			wantProcessingError: "usage interval crosses a price version boundary",
			wantNextWork:        false,
			wantState: priceBoundaryState{
				Groups: 0, Units: "0", PriceVersionID: "", UsageMonth: "", BillingMonth: "",
				GrossTicks: "0", AllocatedCreditTicks: "0", BookedCents: 0,
				Ratings: 0, MonthlyRows: 0, MonthlyGrossTicks: "0",
				CreditDebits: 0, CreditDebitTicks: "0", CreditBalanceTicks: "2000000", StateVersion: 0,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			if _, err := pool.Exec(ctx, "UPDATE customer_billing_state SET credit_balance_ticks=$2::numeric WHERE customer_id=$1", tc.customerID, tc.initialCreditTicks); err != nil {
				t.Fatalf("Prepare credit for customer=%s ticks=%s: %v", tc.customerID, tc.initialCreditTicks, err)
			}

			event := usage.Event{
				Source: "price-boundary", EventID: "one", SchemaVersion: 1,
				CustomerID: tc.customerID, SandboxID: "sandbox", Metric: "cpu_seconds",
				PeriodStart: parseBillingTime(t, tc.periodStart), PeriodEnd: parseBillingTime(t, tc.periodEnd), Units: tc.units,
			}
			if err := inbox.NewPostgres(pool, time.Second).InsertBatch(ctx, []usage.Event{event}, parseBillingTime(t, tc.receivedAt)); err != nil {
				t.Fatalf("Insert price boundary receipt %+v received_at=%s: %v", event, tc.receivedAt, err)
			}

			store := billing.NewStore(pool)
			worked, err := store.ProcessBatch(ctx)
			if err != tc.wantError {
				t.Fatalf("ProcessBatch(%+v) error=%v; want %v", event, err, tc.wantError)
			}
			if worked != tc.wantWork {
				t.Errorf("ProcessBatch(%+v) work=%t; want %t", event, worked, tc.wantWork)
			}

			var processed bool
			var processingError string
			if err := pool.QueryRow(ctx, "SELECT processed_at IS NOT NULL,COALESCE(processing_error,'') FROM usage_inbox WHERE source=$1 AND event_id=$2", event.Source, event.EventID).Scan(&processed, &processingError); err != nil {
				t.Fatalf("Read price boundary receipt state for %+v: %v", event, err)
			}
			if processingError != tc.wantProcessingError {
				t.Errorf("ProcessBatch(%+v) processing_error=%q; want %q", event, processingError, tc.wantProcessingError)
			}
			if processed != tc.wantProcessed {
				t.Errorf("ProcessBatch(%+v) processed=%t; want %t", event, processed, tc.wantProcessed)
			}

			worked, err = store.ProcessBatch(ctx)
			if err != tc.wantError {
				t.Fatalf("ProcessBatch after handling %+v error=%v; want %v", event, err, tc.wantError)
			}
			if worked != tc.wantNextWork {
				t.Errorf("ProcessBatch after handling %+v work=%t; want %t", event, worked, tc.wantNextWork)
			}

			state, err := readPriceBoundaryState(ctx, pool, tc.customerID)
			if err != nil {
				t.Fatalf("Read financial state after ProcessBatch(%+v): %v", event, err)
			}
			if !reflect.DeepEqual(state, tc.wantState) {
				t.Errorf("ProcessBatch(%+v) financial state=%+v; want %+v", event, state, tc.wantState)
			}
		})
	}
}

// readPriceBoundaryState decodes all persisted financial outcomes in one private
// scenario schema; it neither calculates expected amounts nor changes storage.
func readPriceBoundaryState(ctx context.Context, pool *pgxpool.Pool, customer string) (priceBoundaryState, error) {
	var state priceBoundaryState
	err := pool.QueryRow(ctx, `SELECT count(*),COALESCE(sum(total_units),0)::text,
        COALESCE(min(price_version_id),''),COALESCE(min(usage_month)::text,''),COALESCE(min(billing_month)::text,''),
        COALESCE(sum(exact_charge_ticks),0)::text,COALESCE(sum(allocated_credit_ticks),0)::text,
        COALESCE(sum(booked_charge_cents),0)::bigint,
        (SELECT count(*) FROM usage_ratings),
        (SELECT count(*) FROM monthly_usage),
        (SELECT COALESCE(sum(gross_charge_ticks),0)::text FROM monthly_usage),
        (SELECT count(*) FROM credit_entries WHERE group_id IS NOT NULL),
        (SELECT COALESCE(sum(amount_ticks),0)::text FROM credit_entries WHERE group_id IS NOT NULL),
        (SELECT credit_balance_ticks::text FROM customer_billing_state WHERE customer_id=$1),
        (SELECT state_version FROM customer_billing_state WHERE customer_id=$1)
        FROM rated_usage_groups`, customer).Scan(&state.Groups, &state.Units, &state.PriceVersionID,
		&state.UsageMonth, &state.BillingMonth, &state.GrossTicks, &state.AllocatedCreditTicks,
		&state.BookedCents, &state.Ratings, &state.MonthlyRows, &state.MonthlyGrossTicks,
		&state.CreditDebits, &state.CreditDebitTicks, &state.CreditBalanceTicks, &state.StateVersion)
	return state, err
}
