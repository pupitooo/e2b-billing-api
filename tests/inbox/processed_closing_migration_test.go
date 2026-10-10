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
	"e2b/billing-api/internal/inbox"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestProcessedUsageClosingMigration upgrades existing pending cohorts without
// changing raw usage, credit, rated groups, invoice snapshots or numbering. Both
// time zones retain pending errors and protect the already invoiced group.
func TestProcessedUsageClosingMigration(t *testing.T) {
	cases := []struct {
		name                                  string
		timeZone                              string
		pendingError                          *string
		wantRetiredTables                     bool
		wantEarlierUsageIndex                 bool
		wantInvoices, wantGroups, wantRatings int
		wantCredit                            string
		wantNextNumber, wantVersion           int64
		wantPendingProcessed                  bool
	}{
		{name: "UTC pending cohort", timeZone: "UTC", wantRetiredTables: true, wantEarlierUsageIndex: true, wantInvoices: 1, wantGroups: 1, wantRatings: 1, wantCredit: "2000000", wantNextNumber: 2, wantVersion: 3, wantPendingProcessed: false},
		{name: "UTC quarantined cohort", timeZone: "UTC", pendingError: stringPointer("investigated failure"), wantRetiredTables: true, wantEarlierUsageIndex: true, wantInvoices: 1, wantGroups: 1, wantRatings: 1, wantCredit: "2000000", wantNextNumber: 2, wantVersion: 3, wantPendingProcessed: false},
		{name: "Shanghai pending cohort", timeZone: "Asia/Shanghai", wantRetiredTables: true, wantEarlierUsageIndex: true, wantInvoices: 1, wantGroups: 1, wantRatings: 1, wantCredit: "2000000", wantNextNumber: 2, wantVersion: 3, wantPendingProcessed: false},
		{name: "Shanghai quarantined cohort", timeZone: "Asia/Shanghai", pendingError: stringPointer("investigated failure"), wantRetiredTables: true, wantEarlierUsageIndex: true, wantInvoices: 1, wantGroups: 1, wantRatings: 1, wantCredit: "2000000", wantNextNumber: 2, wantVersion: 3, wantPendingProcessed: false},
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
			if err := store.GrantCredit(ctx, "acme", billing.CreditGrant{OperationID: "migration-credit", AmountCents: 6, RecordedAt: parseBillingTime(t, "2026-10-01T00:00:00Z")}); err != nil {
				t.Fatal(err)
			}
			insertMeasuredHours(t, inbox.NewPostgres(pool, time.Second), "acme", []measuredHour{{eventID: "accounted", start: "2026-10-10T12:00:00Z", units: 1_000_000}}, "2026-10-11T00:00:00Z")
			processUsageSteps(t, store, []bool{true})
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
