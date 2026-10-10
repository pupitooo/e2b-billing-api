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
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestProcessedUsageClosingMigration upgrades existing pending cohorts without
// changing raw usage, credit, rated groups, invoice snapshots or numbering. Both
// time zones retain pending errors and protect the already invoiced group.
func TestProcessedUsageClosingMigration(t *testing.T) {
	// Build historical rows directly: the current accounting code requires
	// migration 012, while this scenario specifically upgrades migration 008.
	const preUpgradeAccountingSQL = `INSERT INTO usage_inbox
		(source,event_id,schema_version,customer_id,sandbox_id,metric,period_start,period_end,units,received_at,processed_at)
		VALUES ('upgrade-history','accounted',1,'acme','sandbox','cpu_seconds','2026-10-10T12:00:00Z','2026-10-10T13:00:00Z',1000000,'2026-10-11T00:00:00Z','2026-10-11T00:00:00Z');
		INSERT INTO rated_usage_groups
		(group_id,customer_id,price_version_id,metric,usage_month,billing_month,total_units,exact_charge_ticks,booked_charge_cents,allocated_credit_ticks)
		VALUES ('group/earlier','acme','cpu-acme-2026-10-01','cpu_seconds','2026-10-01','2026-10-01',1000000,4000000,4,4000000);
		INSERT INTO usage_ratings (source,event_id,group_id) VALUES ('upgrade-history','accounted','group/earlier');
		INSERT INTO credit_entries (credit_entry_id,customer_id,operation_id,group_id,amount_ticks,recorded_at) VALUES
		('grant/earlier','acme','grant/migration-credit',NULL,6000000,'2026-10-01T00:00:00Z'),
		('usage/earlier','acme','usage/earlier','group/earlier',-4000000,'2026-10-11T00:00:00Z');
		INSERT INTO monthly_usage (customer_id,usage_month,gross_charge_ticks) VALUES ('acme','2026-10-01',4000000);
		UPDATE customer_billing_state SET credit_balance_ticks=2000000,state_version=2 WHERE customer_id='acme'`
	cases := []struct {
		name                                  string
		setupSQL                              string
		timeZone                              string
		pendingError                          *string
		wantRetiredTables                     bool
		wantEarlierUsageIndex                 bool
		wantInvoices, wantGroups, wantRatings int
		wantCredit                            string
		wantNextNumber, wantVersion           int64
		wantPendingProcessed                  bool
	}{
		{name: "UTC pending cohort", setupSQL: preUpgradeAccountingSQL, timeZone: "UTC", wantRetiredTables: true, wantEarlierUsageIndex: true, wantInvoices: 1, wantGroups: 1, wantRatings: 1, wantCredit: "2000000", wantNextNumber: 2, wantVersion: 3, wantPendingProcessed: false},
		{name: "UTC quarantined cohort", setupSQL: preUpgradeAccountingSQL, timeZone: "UTC", pendingError: stringPointer("investigated failure"), wantRetiredTables: true, wantEarlierUsageIndex: true, wantInvoices: 1, wantGroups: 1, wantRatings: 1, wantCredit: "2000000", wantNextNumber: 2, wantVersion: 3, wantPendingProcessed: false},
		{name: "Shanghai pending cohort", setupSQL: preUpgradeAccountingSQL, timeZone: "Asia/Shanghai", wantRetiredTables: true, wantEarlierUsageIndex: true, wantInvoices: 1, wantGroups: 1, wantRatings: 1, wantCredit: "2000000", wantNextNumber: 2, wantVersion: 3, wantPendingProcessed: false},
		{name: "Shanghai quarantined cohort", setupSQL: preUpgradeAccountingSQL, timeZone: "Asia/Shanghai", pendingError: stringPointer("investigated failure"), wantRetiredTables: true, wantEarlierUsageIndex: true, wantInvoices: 1, wantGroups: 1, wantRatings: 1, wantCredit: "2000000", wantNextNumber: 2, wantVersion: 3, wantPendingProcessed: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := testDatabase(t, nil)
			ctx := context.Background()
			config := pool.Config()
			config.ConnConfig.RuntimeParams["timezone"] = tc.timeZone
			pool, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			var zone string
			if err := pool.QueryRow(ctx, "SHOW timezone").Scan(&zone); err != nil {
				t.Fatal(err)
			}
			if zone != tc.timeZone {
				t.Fatalf("Migration session timezone=%q; want %q", zone, tc.timeZone)
			}

			files, err := filepath.Glob("../../migrations/[0-9][0-9][0-9]_*.sql")
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range files[1:8] {
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, string(data)); err != nil {
					t.Fatalf("Apply pre-upgrade migration %s: %v", file, err)
				}
			}
			invoiceTime := parseBillingTime(t, "2026-12-01T00:00:00Z")
			store := billing.NewStoreWithClock(pool, func() time.Time { return invoiceTime })
			if _, err := pool.Exec(ctx, tc.setupSQL); err != nil {
				t.Fatal(err)
			}
			original, err := store.CloseMonth(ctx, "acme", "2026-10")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO usage_inbox
                (source,event_id,schema_version,customer_id,sandbox_id,metric,period_start,period_end,units,received_at,processing_error)
                VALUES ('upgrade','pending',1,'acme','sandbox','cpu_seconds','2026-10-10T13:00:00Z','2026-10-10T14:00:00Z',1000000,'2026-10-11T00:00:00Z',$1)`, tc.pendingError); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO invoice_closings VALUES ('acme','2026-11-01','2026-12-01T00:00:00Z');
                INSERT INTO invoice_closing_receipts VALUES ('acme','2026-11-01','upgrade','pending')`); err != nil {
				t.Fatal(err)
			}
			if tc.pendingError != nil {
				if _, err := pool.Exec(ctx, "INSERT INTO invoice_closing_exclusions VALUES ('acme','2026-11-01','upgrade','pending',$1)", *tc.pendingError); err != nil {
					t.Fatal(err)
				}
			}
			migration, err := os.ReadFile("../../migrations/009_processed_usage_closing.sql")
			if err != nil {
				t.Fatal(err)
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)

			if _, err := tx.Exec(ctx, string(migration)); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}

			var retired, earlierUsageIndex, processed bool
			var invoices, groups, ratings int
			var next, version int64
			var credit string
			var pendingError *string
			if err := pool.QueryRow(ctx, `SELECT
                to_regclass('invoice_closings') IS NULL AND to_regclass('invoice_closing_receipts') IS NULL AND to_regclass('invoice_closing_exclusions') IS NULL,
                to_regclass('usage_inbox_customer_period_start_idx') IS NOT NULL,
                (SELECT count(*) FROM invoices),(SELECT count(*) FROM rated_usage_groups),(SELECT count(*) FROM usage_ratings),
                credit_balance_ticks::text,next_invoice_number,state_version,
                (SELECT processed_at IS NOT NULL FROM usage_inbox WHERE source='upgrade'),
                (SELECT processing_error FROM usage_inbox WHERE source='upgrade')
                FROM customer_billing_state WHERE customer_id='acme'`).Scan(&retired, &earlierUsageIndex, &invoices, &groups, &ratings, &credit, &next, &version, &processed, &pendingError); err != nil {
				t.Fatal(err)
			}
			if retired != tc.wantRetiredTables || earlierUsageIndex != tc.wantEarlierUsageIndex || invoices != tc.wantInvoices || groups != tc.wantGroups || ratings != tc.wantRatings || credit != tc.wantCredit || next != tc.wantNextNumber || version != tc.wantVersion || processed != tc.wantPendingProcessed || !reflect.DeepEqual(pendingError, tc.pendingError) {
				t.Errorf("Migration state retired=%t earlierUsageIndex=%t invoices=%d groups=%d ratings=%d credit=%s next=%d version=%d processed=%t error=%v; want %+v", retired, earlierUsageIndex, invoices, groups, ratings, credit, next, version, processed, pendingError, tc)
			}

			retry, err := store.CloseMonth(ctx, "acme", "2026-10")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(retry, original) {
				t.Errorf("Migrated invoice=%+v; want original %+v", retry, original)
			}
			if _, err := pool.Exec(ctx, "UPDATE rated_usage_groups SET total_units=0"); err == nil {
				t.Fatal("Migrated invoiced group changed; want immutable")
			}
		})
	}
}
