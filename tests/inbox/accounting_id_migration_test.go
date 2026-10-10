//go:build integration

package inbox_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
)

// TestAccountingIDSequencesMigration upgrades private pre-012 schemas with frozen
// accounting history. It preserves every reference and invoice, starts empty
// numeric namespaces at one, and reserves earlier canonical numeric IDs.
func TestAccountingIDSequencesMigration(t *testing.T) {
	cases := []struct {
		name             string
		oldGroupID       string
		oldGrantID       string
		wantNextGroups   []string
		wantNextCredits  []string
		wantState        accountingIdentityState
		wantInvoiceCents int64
		wantFrozenGroups int
	}{
		{
			name: "earlier opaque IDs start new sequences at one", oldGroupID: "group/earlier", oldGrantID: "grant/earlier",
			wantNextGroups: []string{"grp_1", "grp_2"}, wantNextCredits: []string{"crd_1", "crd_2"}, wantInvoiceCents: 0, wantFrozenGroups: 1,
			wantState: accountingIdentityState{groupIDs: "group/earlier", creditIDs: "grant/earlier,usage/earlier", units: "250000", grossTicks: "1000000",
				allocatedTicks: "1000000", monthlyTicks: "1000000", balanceTicks: "1000000", ratings: 1, stateVersion: 3, groupSequence: 2, creditSequence: 2},
		},
		{
			name: "earlier decimal IDs are reserved", oldGroupID: "grp_7", oldGrantID: "crd_9",
			wantNextGroups: []string{"grp_8", "grp_9"}, wantNextCredits: []string{"crd_10", "crd_11"}, wantInvoiceCents: 0, wantFrozenGroups: 1,
			wantState: accountingIdentityState{groupIDs: "grp_7", creditIDs: "crd_9,usage/earlier", units: "250000", grossTicks: "1000000",
				allocatedTicks: "1000000", monthlyTicks: "1000000", balanceTicks: "1000000", ratings: 1, stateVersion: 3, groupSequence: 9, creditSequence: 11},
		},
		{
			name: "last bigint numbers remain usable", oldGroupID: "grp_9223372036854775805", oldGrantID: "crd_9223372036854775805",
			wantNextGroups:  []string{"grp_9223372036854775806", "grp_9223372036854775807"},
			wantNextCredits: []string{"crd_9223372036854775806", "crd_9223372036854775807"}, wantInvoiceCents: 0, wantFrozenGroups: 1,
			wantState: accountingIdentityState{groupIDs: "grp_9223372036854775805", creditIDs: "crd_9223372036854775805,usage/earlier",
				units: "250000", grossTicks: "1000000", allocatedTicks: "1000000", monthlyTicks: "1000000", balanceTicks: "1000000",
				ratings: 1, stateVersion: 3, groupSequence: 9_223_372_036_854_775_807, creditSequence: 9_223_372_036_854_775_807},
		},
		{
			name: "oversized legacy suffixes remain opaque", oldGroupID: "grp_999999999999999999999999999999", oldGrantID: "crd_999999999999999999999999999999",
			wantNextGroups: []string{"grp_1", "grp_2"}, wantNextCredits: []string{"crd_1", "crd_2"}, wantInvoiceCents: 0, wantFrozenGroups: 1,
			wantState: accountingIdentityState{groupIDs: "grp_999999999999999999999999999999", creditIDs: "crd_999999999999999999999999999999,usage/earlier",
				units: "250000", grossTicks: "1000000", allocatedTicks: "1000000", monthlyTicks: "1000000", balanceTicks: "1000000",
				ratings: 1, stateVersion: 3, groupSequence: 2, creditSequence: 2},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := testDatabase(t, nil)
			ctx := context.Background()
			files, err := filepath.Glob("../../migrations/[0-9][0-9][0-9]_*.sql")
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range files[1:] {
				if filepath.Base(file) == "012_accounting_id_sequences.sql" {
					break
				}
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, string(data)); err != nil {
					t.Fatalf("Apply pre-upgrade migration %s: %v", file, err)
				}
			}
			if _, err := pool.Exec(ctx, `INSERT INTO rated_usage_groups VALUES
				($1,'acme','cpu-acme-2026-10-01','cpu_seconds','2026-10-01','2026-10-01',250000,1000000,1,1000000)`, tc.oldGroupID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "INSERT INTO credit_entries VALUES ($1,'acme','grant/earlier',NULL,2000000,'2026-10-01T00:00:00Z')", tc.oldGrantID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "INSERT INTO credit_entries VALUES ('usage/earlier','acme','usage/earlier',$1,-1000000,'2026-10-10T13:00:00Z')", tc.oldGroupID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO usage_inbox VALUES
				('id-migration','earlier',1,'acme','sandbox','cpu_seconds','2026-10-10T12:00:00Z','2026-10-10T13:00:00Z',250000,'2026-10-10T13:00:00Z','2026-10-10T13:00:00Z',NULL);
				INSERT INTO monthly_usage VALUES ('acme','2026-10-01',1000000);
				UPDATE customer_billing_state SET credit_balance_ticks=1000000,state_version=2 WHERE customer_id='acme'`); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "INSERT INTO usage_ratings VALUES ('id-migration','earlier',$1)", tc.oldGroupID); err != nil {
				t.Fatal(err)
			}
			store := billing.NewStoreWithClock(pool, func() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) })
			original, err := store.CloseMonth(ctx, "acme", "2026-10")
			if err != nil {
				t.Fatal(err)
			}
			if original.TotalCents != tc.wantInvoiceCents {
				t.Fatalf("Pre-upgrade invoice cents=%d; want %d", original.TotalCents, tc.wantInvoiceCents)
			}
			migration, err := os.ReadFile("../../migrations/012_accounting_id_sequences.sql")
			if err != nil {
				t.Fatal(err)
			}

			if _, err := pool.Exec(ctx, string(migration)); err != nil {
				t.Fatalf("Apply migration 012: %v", err)
			}

			for _, wantID := range tc.wantNextGroups {
				var actual string
				if err := pool.QueryRow(ctx, "SELECT 'grp_' || nextval('rated_usage_group_id_seq')::text").Scan(&actual); err != nil {
					t.Fatal(err)
				}
				if actual != wantID {
					t.Fatalf("Next group ID after migration=%q; want %q", actual, wantID)
				}
			}
			for _, wantID := range tc.wantNextCredits {
				var actual string
				if err := pool.QueryRow(ctx, "SELECT 'crd_' || nextval('credit_entry_id_seq')::text").Scan(&actual); err != nil {
					t.Fatal(err)
				}
				if actual != wantID {
					t.Fatalf("Next credit entry ID after migration=%q; want %q", actual, wantID)
				}
			}
			frozen, err := store.Invoice(ctx, "acme", "2026-10")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(frozen, original) {
				t.Fatalf("Invoice after migration=%+v; want original %+v", frozen, original)
			}
			var frozenGroups int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM invoiced_usage_groups WHERE group_id=$1", tc.oldGroupID).Scan(&frozenGroups); err != nil {
				t.Fatal(err)
			}
			if frozenGroups != tc.wantFrozenGroups {
				t.Fatalf("Frozen references for group_id=%q: %d; want %d", tc.oldGroupID, frozenGroups, tc.wantFrozenGroups)
			}
			assertAccountingIdentityState(t, pool, tc.wantState)
		})
	}
}
