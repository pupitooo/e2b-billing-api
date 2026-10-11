//go:build integration

package inbox_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/postgres"
)

// testPartitionedUsage compares unsplit and split increments only under identical
// price and invoice conditions, with any grant entirely before processing. No
// grant or closure may interleave the fragments; all expected values are literal.
func testPartitionedUsage(t *testing.T) {
	type partition struct {
		name  string
		units []int64
	}
	cases := []struct {
		name                                          string
		initialGrantCents                             int64
		partitions                                    []partition
		wantUnits, wantGross, wantCredit, wantBalance string
		wantBookedCents, wantTotalCents               int64
		wantLines                                     []billing.InvoiceLine
	}{
		{name: "half-cent fragments round as one group", initialGrantCents: 0, partitions: []partition{{name: "one receipt", units: []int64{250_000}}, {name: "two exact half-cent fragments", units: []int64{125_000, 125_000}}, {name: "three fragments", units: []int64{124_999, 1, 125_000}}}, wantUnits: "250000", wantGross: "1000000", wantCredit: "0", wantBalance: "0", wantBookedCents: 1, wantTotalCents: 1, wantLines: []billing.InvoiceLine{
			{Kind: "usage", Description: "Usage", PriceVersionID: "cpu-acme-2026-10-01", Metric: "cpu_seconds", UsageMonth: "2026-10", Units: "250000", GrossMicrocents: "1000000", CreditMicrocents: "0", AmountCents: 1}, {Kind: "credit", Description: "Used credit", AmountCents: 0},
		}},
		{name: "upfront partial credit preserves net half-cent", initialGrantCents: 1, partitions: []partition{{name: "one receipt", units: []int64{375_000}}, {name: "four fragments", units: []int64{124_999, 1, 125_000, 125_000}}}, wantUnits: "375000", wantGross: "1500000", wantCredit: "1000000", wantBalance: "0", wantBookedCents: 2, wantTotalCents: 1, wantLines: []billing.InvoiceLine{
			{Kind: "usage", Description: "Usage", PriceVersionID: "cpu-acme-2026-10-01", Metric: "cpu_seconds", UsageMonth: "2026-10", Units: "375000", GrossMicrocents: "1500000", CreditMicrocents: "1000000", AmountCents: 2}, {Kind: "credit", Description: "Used credit", AmountCents: -1},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, partition := range tc.partitions {
				t.Run(partition.name, func(t *testing.T) {
					pool := billingDatabase(t)
					store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return parseBillingTime(t, "2026-11-01T00:00:00Z") }})
					ctx := context.Background()
					if tc.initialGrantCents > 0 {
						if err := store.GrantCredit(ctx, "acme", billing.CreditGrant{IdempotencyKey: "upfront", AmountCents: tc.initialGrantCents}); err != nil {
							t.Fatal(err)
						}
					}
					var hours []measuredHour
					for index, units := range partition.units {
						hours = append(hours, measuredHour{eventID: fmt.Sprint(index), start: "2026-10-10T12:00:00Z", units: units})
					}
					insertMeasuredHours(t, store, "acme", hours, "2026-10-11T00:00:00Z")

					for range partition.units {
						processUsageSteps(t, store, []bool{true})
					}
					processUsageSteps(t, store, []bool{false})

					var units, gross, credit, balance string
					var booked int64
					if err := pool.QueryRow(ctx, `SELECT total_units::text,exact_charge_microcents::text,allocated_credit_microcents::text,booked_charge_cents,(SELECT credit_balance_microcents::text FROM customer_billing_state WHERE customer_id='acme') FROM rated_usage_groups`).Scan(&units, &gross, &credit, &booked, &balance); err != nil {
						t.Fatal(err)
					}
					if units != tc.wantUnits || gross != tc.wantGross || credit != tc.wantCredit || booked != tc.wantBookedCents || balance != tc.wantBalance {
						t.Fatalf("Partition=%+v group units=%s gross=%s credit=%s booked=%d balance=%s; want %s %s %s %d %s", partition, units, gross, credit, booked, balance, tc.wantUnits, tc.wantGross, tc.wantCredit, tc.wantBookedCents, tc.wantBalance)
					}
					assertAccountingInvariants(t, pool)
					invoice, err := store.CloseMonth(ctx, "acme", "2026-10")
					if err != nil {
						t.Fatal(err)
					}
					if invoice.TotalCents != tc.wantTotalCents || invoice.GrossUsageMicrocents != tc.wantGross || invoice.CreditUsedMicrocents != tc.wantCredit || !reflect.DeepEqual(invoice.Lines, tc.wantLines) {
						t.Fatalf("Partition=%+v invoice=%+v; want total=%d gross=%s credit=%s lines=%+v", partition, invoice, tc.wantTotalCents, tc.wantGross, tc.wantCredit, tc.wantLines)
					}
				})
			}
		})
	}
}
