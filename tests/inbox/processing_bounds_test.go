//go:build integration

package inbox_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"e2b/billing-api/internal/accounting"
	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/usage"
)

// testProcessingBounds adds isolated ProcessBatch scenarios for the last valid
// billing month and the maximum whole-cent group total. Crossing either bound
// must quarantine the new receipt without changing any financial projection.
// Prior receipts are explicit setup for the cumulative-overflow scenario.
func testProcessingBounds(t *testing.T) {
	t.Helper()

	cases := []struct {
		name                 string
		customerID           string
		priceVersionID       string
		pricePerMillionCents int64
		priceEffectiveFrom   string
		periodStart          string
		periodEnd            string
		receivedAt           string
		closedMonth          string
		priorUnits           []int64
		units                int64
		availableCreditTicks string
		wantError            bool
		wantWork             bool
		wantProcessed        bool
		wantProcessingError  string
		wantNextWork         bool
		wantState            priceBoundaryState
	}{
		{
			name:                 "last supported billing month accounts while open",
			customerID:           "acme",
			priceVersionID:       "processing-boundary-price",
			pricePerMillionCents: 4,
			priceEffectiveFrom:   "2026-10-02T00:00:00Z",
			periodStart:          "9999-12-10T12:00:00Z",
			periodEnd:            "9999-12-10T13:00:00Z",
			receivedAt:           "9999-12-11T00:00:00Z",
			units:                1_000_000,
			availableCreditTicks: "1000000",
			wantError:            false,
			wantWork:             true,
			wantProcessed:        true,
			wantProcessingError:  "",
			wantNextWork:         false,
			wantState: priceBoundaryState{
				Groups: 1, Units: "1000000", PriceVersionID: "processing-boundary-price",
				UsageMonth: "9999-12-01", BillingMonth: "9999-12-01",
				GrossTicks: "4000000", AllocatedCreditTicks: "1000000", BookedCents: 4,
				Ratings: 1, MonthlyRows: 1, MonthlyGrossTicks: "4000000",
				CreditDebits: 1, CreditDebitTicks: "-1000000", CreditBalanceTicks: "0", StateVersion: 1,
			},
		},
		{
			name:                 "closed last billing month quarantines without financial effects",
			customerID:           "acme",
			priceVersionID:       "processing-boundary-price",
			pricePerMillionCents: 4,
			priceEffectiveFrom:   "2026-10-02T00:00:00Z",
			periodStart:          "9999-12-10T12:00:00Z",
			periodEnd:            "9999-12-10T13:00:00Z",
			receivedAt:           "9999-12-11T00:00:00Z",
			closedMonth:          "9999-12-01",
			units:                1_000_000,
			availableCreditTicks: "1000000",
			wantError:            false,
			wantWork:             true,
			wantProcessed:        false,
			wantProcessingError:  "no representable open billing month",
			wantNextWork:         false,
			wantState: priceBoundaryState{
				Groups: 0, Units: "0", PriceVersionID: "", UsageMonth: "", BillingMonth: "",
				GrossTicks: "0", AllocatedCreditTicks: "0", BookedCents: 0,
				Ratings: 0, MonthlyRows: 0, MonthlyGrossTicks: "0",
				CreditDebits: 0, CreditDebitTicks: "0", CreditBalanceTicks: "1000000", StateVersion: 0,
			},
		},
		{
			name:                 "skipped November behind the final closure quarantines without financial effects",
			customerID:           "acme",
			priceVersionID:       "processing-boundary-price",
			pricePerMillionCents: 4,
			priceEffectiveFrom:   "2026-10-02T00:00:00Z",
			periodStart:          "9999-11-10T12:00:00Z",
			periodEnd:            "9999-11-10T13:00:00Z",
			receivedAt:           "9999-11-11T00:00:00Z",
			closedMonth:          "9999-12-01",
			units:                1_000_000,
			availableCreditTicks: "1000000",
			wantError:            false,
			wantWork:             true,
			wantProcessed:        false,
			wantProcessingError:  "no representable open billing month",
			wantNextWork:         false,
			wantState: priceBoundaryState{
				Groups: 0, Units: "0", PriceVersionID: "", UsageMonth: "", BillingMonth: "",
				GrossTicks: "0", AllocatedCreditTicks: "0", BookedCents: 0,
				Ratings: 0, MonthlyRows: 0, MonthlyGrossTicks: "0",
				CreditDebits: 0, CreditDebitTicks: "0", CreditBalanceTicks: "1000000", StateVersion: 0,
			},
		},
		{
			name:                 "maximum whole-cent group total remains valid",
			customerID:           "acme",
			priceVersionID:       "processing-boundary-price",
			pricePerMillionCents: 9_223_372_036_854_775_807,
			priceEffectiveFrom:   "2026-10-02T00:00:00Z",
			periodStart:          "2026-10-10T12:00:00Z",
			periodEnd:            "2026-10-10T13:00:00Z",
			receivedAt:           "2026-10-11T00:00:00Z",
			units:                1_000_000,
			availableCreditTicks: "1000000",
			wantError:            false,
			wantWork:             true,
			wantProcessed:        true,
			wantProcessingError:  "",
			wantNextWork:         false,
			wantState: priceBoundaryState{
				Groups: 1, Units: "1000000", PriceVersionID: "processing-boundary-price",
				UsageMonth: "2026-10-01", BillingMonth: "2026-10-01",
				GrossTicks: "9223372036854775807000000", AllocatedCreditTicks: "1000000", BookedCents: 9_223_372_036_854_775_807,
				Ratings: 1, MonthlyRows: 1, MonthlyGrossTicks: "9223372036854775807000000",
				CreditDebits: 1, CreditDebitTicks: "-1000000", CreditBalanceTicks: "0", StateVersion: 1,
			},
		},
		{
			name:                 "cumulative cent overflow preserves prior accounting and available credit",
			customerID:           "acme",
			priceVersionID:       "processing-boundary-price",
			pricePerMillionCents: 9_223_372_036_854_775_807,
			priceEffectiveFrom:   "2026-10-02T00:00:00Z",
			periodStart:          "2026-10-10T12:00:00Z",
			periodEnd:            "2026-10-10T13:00:00Z",
			receivedAt:           "2026-10-11T00:00:00Z",
			priorUnits:           []int64{1_000_000},
			units:                1,
			availableCreditTicks: "1000000",
			wantError:            false,
			wantWork:             true,
			wantProcessed:        false,
			wantProcessingError:  "rounded cents exceed the signed 64-bit range",
			wantNextWork:         false,
			wantState: priceBoundaryState{
				Groups: 1, Units: "1000000", PriceVersionID: "processing-boundary-price",
				UsageMonth: "2026-10-01", BillingMonth: "2026-10-01",
				GrossTicks: "9223372036854775807000000", AllocatedCreditTicks: "0", BookedCents: 9_223_372_036_854_775_807,
				Ratings: 1, MonthlyRows: 1, MonthlyGrossTicks: "9223372036854775807000000",
				CreditDebits: 0, CreditDebitTicks: "0", CreditBalanceTicks: "1000000", StateVersion: 1,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			store := billing.NewStoreWithClock(pool, func() time.Time { return parseBillingTime(t, "2026-10-01T00:00:00Z") })
			transport := inbox.NewPostgres(pool, time.Second)
			price := accounting.PriceVersion{
				ID: tc.priceVersionID, CustomerID: tc.customerID, Metric: "cpu_seconds",
				EffectiveFrom: parseBillingTime(t, tc.priceEffectiveFrom), PricePerMillionCents: tc.pricePerMillionCents,
			}
			if _, err := pool.Exec(ctx, `INSERT INTO price_versions VALUES ($1,$2,$3,$4,$5)`, price.ID, price.CustomerID, price.Metric, price.PricePerMillionCents, price.EffectiveFrom); err != nil {
				t.Fatalf("Prepare price %+v: %v", price, err)
			}

			event := usage.Event{
				Source: "processing-bounds", EventID: "current", SchemaVersion: 1,
				CustomerID: tc.customerID, SandboxID: "sandbox", Metric: "cpu_seconds",
				PeriodStart: parseBillingTime(t, tc.periodStart), PeriodEnd: parseBillingTime(t, tc.periodEnd), Units: tc.units,
			}
			receivedAt := parseBillingTime(t, tc.receivedAt)
			for index, units := range tc.priorUnits {
				prior := event
				prior.EventID = fmt.Sprintf("prior-%d", index)
				prior.Units = units
				if err := transport.InsertBatch(ctx, []usage.Event{prior}, receivedAt); err != nil {
					t.Fatalf("Prepare prior receipt %+v: %v", prior, err)
				}

				worked, err := store.ProcessBatch(ctx)
				if err != nil {
					t.Fatalf("Account for prior receipt %+v: %v", prior, err)
				}
				if !worked {
					t.Fatalf("Account for prior receipt %+v: work=false; want true", prior)
				}
			}

			if _, err := pool.Exec(ctx, "UPDATE customer_billing_state SET credit_balance_ticks=$2::numeric WHERE customer_id=$1", tc.customerID, tc.availableCreditTicks); err != nil {
				t.Fatalf("Prepare available credit for customer=%s ticks=%s: %v", tc.customerID, tc.availableCreditTicks, err)
			}
			if tc.closedMonth != "" {
				if _, err := pool.Exec(ctx, "INSERT INTO closed_billing_months VALUES ($1,$2,$3)", tc.customerID, tc.closedMonth, receivedAt); err != nil {
					t.Fatalf("Prepare closed month=%s for customer=%s: %v", tc.closedMonth, tc.customerID, err)
				}
			}
			if err := transport.InsertBatch(ctx, []usage.Event{event}, receivedAt); err != nil {
				t.Fatalf("Prepare current receipt %+v received_at=%s: %v", event, receivedAt, err)
			}

			worked, err := store.ProcessBatch(ctx)
			if (err != nil) != tc.wantError {
				t.Fatalf("ProcessBatch(%+v) error=%v; wantError %t", event, err, tc.wantError)
			}
			if worked != tc.wantWork {
				t.Errorf("ProcessBatch(%+v) work=%t; want %t", event, worked, tc.wantWork)
			}

			var processed bool
			var processingError string
			if err := pool.QueryRow(ctx, "SELECT processed_at IS NOT NULL,COALESCE(processing_error,'') FROM usage_inbox WHERE source=$1 AND event_id=$2", event.Source, event.EventID).Scan(&processed, &processingError); err != nil {
				t.Fatalf("Read current receipt state for %+v: %v", event, err)
			}
			if processingError != tc.wantProcessingError {
				t.Errorf("ProcessBatch(%+v) processing_error=%q; want %q", event, processingError, tc.wantProcessingError)
			}
			if processed != tc.wantProcessed {
				t.Errorf("ProcessBatch(%+v) processed=%t; want %t", event, processed, tc.wantProcessed)
			}

			worked, err = store.ProcessBatch(ctx)
			if (err != nil) != tc.wantError {
				t.Fatalf("ProcessBatch after handling %+v error=%v; wantError %t", event, err, tc.wantError)
			}
			if worked != tc.wantNextWork {
				t.Errorf("ProcessBatch after handling %+v work=%t; want %t", event, worked, tc.wantNextWork)
			}

			state, err := readPriceBoundaryState(ctx, pool, tc.customerID)
			if err != nil {
				t.Fatalf("Read financial state for customer=%s after receipt %+v: %v", tc.customerID, event, err)
			}
			if !reflect.DeepEqual(state, tc.wantState) {
				t.Errorf("ProcessBatch(%+v) financial state=%+v; want %+v", event, state, tc.wantState)
			}
		})
	}
}
