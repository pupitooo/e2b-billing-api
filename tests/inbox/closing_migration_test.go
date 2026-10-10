//go:build integration

package inbox_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestInvoiceClosingExclusionsMigration upgrades an existing closing cohort in
// an owned schema. Only nonnull errors are backfilled, including an empty error;
// membership and financial state remain unchanged in both configured time zones.
func TestInvoiceClosingExclusionsMigration(t *testing.T) {
	cases := []struct {
		name            string
		timeZone        string
		processingError *string
		wantExclusions  int
		wantCohort      int
		wantRatings     int
		wantCredit      string
	}{
		{name: "UTC pending receipt remains eligible", timeZone: "UTC", wantExclusions: 0, wantCohort: 1, wantRatings: 0, wantCredit: "0"},
		{name: "UTC existing error is permanently excluded", timeZone: "UTC", processingError: stringPointer("investigated failure"), wantExclusions: 1, wantCohort: 1, wantRatings: 0, wantCredit: "0"},
		{name: "UTC empty nonnull error is permanently excluded", timeZone: "UTC", processingError: stringPointer(""), wantExclusions: 1, wantCohort: 1, wantRatings: 0, wantCredit: "0"},
		{name: "Shanghai pending receipt remains eligible", timeZone: "Asia/Shanghai", wantExclusions: 0, wantCohort: 1, wantRatings: 0, wantCredit: "0"},
		{name: "Shanghai existing error is permanently excluded", timeZone: "Asia/Shanghai", processingError: stringPointer("investigated failure"), wantExclusions: 1, wantCohort: 1, wantRatings: 0, wantCredit: "0"},
		{name: "Shanghai empty nonnull error is permanently excluded", timeZone: "Asia/Shanghai", processingError: stringPointer(""), wantExclusions: 1, wantCohort: 1, wantRatings: 0, wantCredit: "0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := testDatabase(t, nil)
			ctx := context.Background()
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, "SELECT set_config('TimeZone',$1,true)", tc.timeZone); err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob("../../migrations/[0-9][0-9][0-9]_*.sql")
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range files[1:7] {
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(ctx, string(data)); err != nil {
					t.Fatalf("Apply pre-upgrade migration %s: %v", file, err)
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO usage_inbox
                (source,event_id,schema_version,customer_id,sandbox_id,metric,period_start,period_end,units,received_at,processing_error)
                VALUES ('migration','existing',1,'cyberdyne','sandbox','cpu_seconds',
                    '2026-10-31T23:59:00Z','2026-11-01T00:00:00Z',0,'2026-11-01T00:00:01Z',$1)`, tc.processingError); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO invoice_closings VALUES ('cyberdyne','2026-10-01','2026-11-01T00:00:02Z');
                INSERT INTO invoice_closing_receipts VALUES ('cyberdyne','2026-10-01','migration','existing')`); err != nil {
				t.Fatal(err)
			}
			migration, err := os.ReadFile("../../migrations/008_invoice_closing_exclusions.sql")
			if err != nil {
				t.Fatal(err)
			}

			if _, err := tx.Exec(ctx, string(migration)); err != nil {
				t.Fatalf("Upgrade existing cohort with error=%v timeZone=%s: %v", tc.processingError, tc.timeZone, err)
			}

			var exclusions, cohort, ratings int
			var credit string
			if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM invoice_closing_exclusions),
                (SELECT count(*) FROM invoice_closing_receipts),(SELECT count(*) FROM usage_ratings),
                credit_balance_ticks::text FROM customer_billing_state WHERE customer_id='cyberdyne'`).Scan(&exclusions, &cohort, &ratings, &credit); err != nil {
				t.Fatal(err)
			}
			if exclusions != tc.wantExclusions || cohort != tc.wantCohort || ratings != tc.wantRatings || credit != tc.wantCredit {
				t.Errorf("Migration with error=%v timeZone=%s: exclusions=%d cohort=%d ratings=%d credit=%s; want %d,%d,%d,%s", tc.processingError, tc.timeZone, exclusions, cohort, ratings, credit, tc.wantExclusions, tc.wantCohort, tc.wantRatings, tc.wantCredit)
			}
			if tc.processingError != nil {
				if _, err := tx.Exec(ctx, "UPDATE usage_inbox SET processing_error=NULL WHERE source='migration' AND event_id='existing'"); err != nil {
					t.Fatal(err)
				}
				var retainedError string
				if err := tx.QueryRow(ctx, "SELECT processing_error FROM invoice_closing_exclusions").Scan(&retainedError); err != nil {
					t.Fatal(err)
				}
				if retainedError != *tc.processingError {
					t.Errorf("Exclusion after release=%q; want original error=%q", retainedError, *tc.processingError)
				}
			}
		})
	}
}
