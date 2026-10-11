//go:build integration

package inbox_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/postgres"
)

// TestAddonLifecycleMigration upgrades an existing purchase and frozen invoice
// in both session zones. Existing IDs and command responses survive; new
// termination omits only later fees and does not rewrite the old snapshot.
func TestAddonLifecycleMigration(t *testing.T) {
	cases := []struct {
		name, timeZone                       string
		wantSubscriptions, wantCancellations int
		wantNovemberCents, wantDecemberCents int64
	}{
		{name: "UTC existing purchase", timeZone: "UTC", wantSubscriptions: 1, wantCancellations: 1, wantNovemberCents: 2000, wantDecemberCents: 0},
		{name: "Shanghai existing purchase", timeZone: "Asia/Shanghai", wantSubscriptions: 1, wantCancellations: 1, wantNovemberCents: 2000, wantDecemberCents: 0},
	}
	original := billing.Invoice{CustomerID: "acme", Month: "2026-10", Number: "ACME-0001", Currency: "USD", CustomerName: "Acme Inc.", Country: "US", BillingAddress: "1 Market St, San Francisco, CA 94105", IssuedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), TotalCents: 2000, GrossUsageMicrocents: "0", CreditUsedMicrocents: "0", Lines: []billing.InvoiceLine{{Kind: "addon", Description: "concurrency_pack", AmountCents: 2000, SubscriptionID: "legacy-pack"}, {Kind: "credit", Description: "Used credit", AmountCents: 0}}}
	purchase := billing.Subscription{SubscriptionID: "legacy-pack", CustomerID: "acme", AddonName: "concurrency_pack", MonthlyPriceCents: 2000, PurchasedAt: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), StartMonth: "2026-10-01"}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			pool := testDatabase(t, nil)
			ctx := context.Background()
			files, err := filepath.Glob("../../migrations/[0-9][0-9][0-9]_*.sql")
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range files[1:] {
				if filepath.Base(file) == "013_addon_cancellations.sql" {
					break
				}
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, string(data)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := pool.Exec(ctx, "INSERT INTO addon_subscriptions VALUES ($1,$2,$3,$4,$5,$6)", purchase.SubscriptionID, purchase.CustomerID, purchase.AddonName, purchase.MonthlyPriceCents, purchase.PurchasedAt, purchase.StartMonth); err != nil {
				t.Fatal(err)
			}
			result, err := json.Marshal(purchase)
			if err != nil {
				t.Fatal(err)
			}
			request, err := json.Marshal(billing.AddonPurchase{AddonName: purchase.AddonName, PurchasedAt: purchase.PurchasedAt})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "INSERT INTO api_idempotency_operations VALUES ('addons/acme','old-key',$1,$2,'2026-10-05T00:00:00Z')", request, result); err != nil {
				t.Fatal(err)
			}
			publishLegacyInvoice(t, pool, original)
			conn, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Release()
			if _, err := conn.Exec(ctx, "SELECT set_config('TimeZone',$1,false)", scenario.timeZone); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile("../../migrations/013_addon_cancellations.sql")
			if err != nil {
				t.Fatal(err)
			}

			if _, err := conn.Exec(ctx, string(data)); err != nil {
				t.Fatalf("migrate cancellation zone=%s: %v", scenario.timeZone, err)
			}

			store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) }})
			replay, err := store.PurchaseAddon(ctx, "acme", "old-key", billing.AddonPurchase{AddonName: "concurrency_pack", PurchasedAt: purchase.PurchasedAt})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(replay, purchase) {
				t.Fatalf("migrated purchase=%+v; want %+v", replay, purchase)
			}
			if _, err := store.CancelAddon(ctx, "acme", "cancel", billing.AddonCancellation{SubscriptionID: purchase.SubscriptionID, CancelledAt: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)}); err != nil {
				t.Fatal(err)
			}
			for _, step := range []struct {
				month     string
				wantCents int64
			}{{month: "2026-11", wantCents: scenario.wantNovemberCents}, {month: "2026-12", wantCents: scenario.wantDecemberCents}} {
				invoice, err := store.CloseMonth(ctx, "acme", step.month)
				if err != nil {
					t.Fatal(err)
				}
				if invoice.TotalCents != step.wantCents {
					t.Fatalf("migrated invoice %s=%+v; want %d cents", step.month, invoice, step.wantCents)
				}
			}
			frozen, err := store.Invoice(ctx, "acme", "2026-10")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(frozen, original) {
				t.Fatalf("migration changed frozen invoice=%+v; want %+v", frozen, original)
			}
			var purchases, ends int
			if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM addon_subscriptions),(SELECT count(*) FROM addon_cancellations)").Scan(&purchases, &ends); err != nil {
				t.Fatal(err)
			}
			if purchases != scenario.wantSubscriptions || ends != scenario.wantCancellations {
				t.Fatalf("migrated rows=%d,%d; want %d,%d", purchases, ends, scenario.wantSubscriptions, scenario.wantCancellations)
			}
		})
	}
}
