//go:build integration

package inbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"e2b/billing-api/internal/accounting"
	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/usage"
)

type measuredHour struct {
	eventID string
	start   string
	units   int64
}
type invoiceExpectation struct {
	month           string
	number          string
	totalCents      int64
	lineCents       []int64
	grossTicks      string
	creditTicks     string
	remainingCredit string
}

// TestCloseMonth reproduces literal assignment invoice values in private schemas.
// Each case keeps usage steps, grants, purchases, and expected invoice lines visible.
func TestCloseMonth(t *testing.T) {
	cases := []struct {
		name         string
		customer     string
		creditCents  int64
		purchaseTime string
		october      []measuredHour
		later        []measuredHour
		wantOctober  invoiceExpectation
		wantNovember *invoiceExpectation
	}{
		{name: "Acme October and late November assignment", customer: "acme", creditCents: 2500, purchaseTime: "2026-10-05T00:00:00Z",
			october:      []measuredHour{{eventID: "oct-first", start: "2026-10-10T12:00:00Z", units: 100_000_000}, {eventID: "oct-second", start: "2026-10-20T12:00:00Z", units: 200_000_000}},
			later:        []measuredHour{{eventID: "late-oct", start: "2026-10-30T12:00:00Z", units: 50_000_000}, {eventID: "nov", start: "2026-11-03T12:00:00Z", units: 100_000_000}},
			wantOctober:  invoiceExpectation{month: "2026-10", number: "ACME-0001", totalCents: 2000, lineCents: []int64{1200, 2000, -1200}, grossTicks: "1200000000", creditTicks: "1200000000", remainingCredit: "1300000000"},
			wantNovember: &invoiceExpectation{month: "2026-11", number: "ACME-0002", totalCents: 2000, lineCents: []int64{200, 400, 2000, -600}, grossTicks: "600000000", creditTicks: "600000000", remainingCredit: "700000000"}},
		{name: "Cyberdyne October historical prices", customer: "cyberdyne",
			october:     []measuredHour{{eventID: "oct-first", start: "2026-10-10T12:00:00Z", units: 123_456_789}, {eventID: "oct-second", start: "2026-10-20T12:00:00Z", units: 200_000_000}},
			wantOctober: invoiceExpectation{month: "2026-10", number: "CYBERDYNE-0001", totalCents: 1817, lineCents: []int64{617, 1200, 0}, grossTicks: "1817283945", creditTicks: "0", remainingCredit: "0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			store := billing.NewStore(pool)
			transport := inbox.NewPostgres(pool, time.Second)
			if tc.creditCents > 0 {
				if err := store.GrantCredit(ctx, tc.customer, billing.CreditGrant{OperationID: "welcome", AmountCents: tc.creditCents, RecordedAt: parseBillingTime(t, "2026-10-01T00:00:00Z")}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.purchaseTime != "" {
				if _, err := store.PurchaseAddon(ctx, tc.customer, billing.AddonPurchase{SubscriptionID: "pack", AddonName: "concurrency_pack", PurchasedAt: parseBillingTime(t, tc.purchaseTime)}); err != nil {
					t.Fatal(err)
				}
			}
			insertMeasuredHours(t, transport, tc.customer, tc.october, "2026-10-31T23:00:00Z")
			october, err := store.CloseMonth(ctx, tc.customer, tc.wantOctober.month)
			if err != nil {
				t.Fatalf("CloseMonth(%s October): %v", tc.customer, err)
			}
			assertInvoiceExpectation(t, store, october, tc.wantOctober)
			before, _ := json.Marshal(october)
			if tc.wantNovember != nil {
				insertMeasuredHours(t, transport, tc.customer, tc.later, "2026-11-04T00:00:00Z")
				november, err := store.CloseMonth(ctx, tc.customer, tc.wantNovember.month)
				if err != nil {
					t.Fatalf("CloseMonth November: %v", err)
				}
				assertInvoiceExpectation(t, store, november, *tc.wantNovember)
			}
			replay, err := store.CloseMonth(ctx, tc.customer, tc.wantOctober.month)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(replay)
			if string(after) != string(before) {
				t.Fatalf("invoice replay changed snapshot: before=%s after=%s", before, after)
			}
			if _, err := pool.Exec(ctx, "UPDATE invoices SET total_cents=0 WHERE customer_id=$1", tc.customer); err == nil {
				t.Fatal("issued invoice update succeeded; want immutable snapshot")
			}
			if _, err := pool.Exec(ctx, "UPDATE rated_usage_groups SET total_units=0 WHERE customer_id=$1", tc.customer); err == nil {
				t.Fatal("frozen group update succeeded; want immutable rating")
			}
		})
	}
	t.Run("concurrent closing allocates one number", func(t *testing.T) {
		scenario := struct {
			workers    int
			month      string
			wantNumber string
			wantCount  int
		}{workers: 8, month: "2026-10", wantNumber: "ACME-0001", wantCount: 1}
		pool := billingDatabase(t)
		store := billing.NewStore(pool)
		var wg sync.WaitGroup
		results := make(chan error, scenario.workers)
		for range scenario.workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				invoice, err := store.CloseMonth(context.Background(), "acme", scenario.month)
				if err == nil && invoice.Number != scenario.wantNumber {
					t.Errorf("concurrent invoice number=%s want %s", invoice.Number, scenario.wantNumber)
				}
				results <- err
			}()
		}
		wg.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Fatalf("concurrent CloseMonth: %v", err)
			}
		}
		var count int
		if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM invoices").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != scenario.wantCount {
			t.Fatalf("concurrent invoice count=%d want %d", count, scenario.wantCount)
		}
	})
	t.Run("fixed cohort resumes after price recovery and excludes later commits", func(t *testing.T) {
		scenario := struct {
			originalUnits     int64
			laterUnits        int64
			wantFirstError    error
			wantOctoberTotal  int64
			wantNovemberTotal int64
			wantCohort        int
		}{originalUnits: 100_000_000, laterUnits: 100_000_000, wantFirstError: billing.ErrConflict, wantOctoberTotal: 400, wantNovemberTotal: 400, wantCohort: 1}
		pool := billingDatabase(t)
		ctx := context.Background()
		store := billing.NewStore(pool)
		transport := inbox.NewPostgres(pool, time.Second)
		if _, err := pool.Exec(ctx, "INSERT INTO metrics VALUES ('unpriced')"); err != nil {
			t.Fatal(err)
		}
		event := usage.Event{Source: "cohort", EventID: "original", SchemaVersion: 1, CustomerID: "acme", SandboxID: "sandbox", Metric: "unpriced", PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z"), Units: scenario.originalUnits}
		if err := transport.InsertBatch(ctx, []usage.Event{event}, event.PeriodEnd); err != nil {
			t.Fatal(err)
		}
		_, err := store.CloseMonth(ctx, "acme", "2026-10")
		if !errors.Is(err, scenario.wantFirstError) {
			t.Fatalf("unpriced cohort error=%v want %v", err, scenario.wantFirstError)
		}
		later := event
		later.EventID = "later"
		later.Metric = "cpu_seconds"
		later.Units = scenario.laterUnits
		// The earlier receipt timestamp cannot bypass the committed cohort cut.
		if err := transport.InsertBatch(ctx, []usage.Event{later}, event.PeriodStart); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ProcessBatch(ctx); err != nil {
			t.Fatal(err)
		}
		if err := store.CreatePrice(ctx, accounting.PriceVersion{ID: "recovered-price", Metric: "unpriced", PricePerMillionCents: 4, EffectiveFrom: parseBillingTime(t, "2026-10-01T00:00:00Z")}); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "UPDATE usage_inbox SET processing_error=NULL WHERE source='cohort' AND event_id='original'"); err != nil {
			t.Fatal(err)
		}
		october, err := store.CloseMonth(ctx, "acme", "2026-10")
		if err != nil {
			t.Fatal(err)
		}
		november, err := store.CloseMonth(ctx, "acme", "2026-11")
		if err != nil {
			t.Fatal(err)
		}
		var cohort int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM invoice_closing_receipts WHERE billing_month='2026-10-01'").Scan(&cohort); err != nil {
			t.Fatal(err)
		}
		if october.TotalCents != scenario.wantOctoberTotal || november.TotalCents != scenario.wantNovemberTotal || cohort != scenario.wantCohort {
			t.Fatalf("recovered cohort: October=%d November=%d members=%d; want %d %d %d", october.TotalCents, november.TotalCents, cohort, scenario.wantOctoberTotal, scenario.wantNovemberTotal, scenario.wantCohort)
		}
	})
}

// insertMeasuredHours preserves all named usage inputs from the scenario; only
// irrelevant transport identifiers and the declared one-hour end are supplied.
func insertMeasuredHours(t testing.TB, transport *inbox.Postgres, customer string, hours []measuredHour, received string) {
	t.Helper()
	for _, hour := range hours {
		start := parseBillingTime(t, hour.start)
		event := usage.Event{Source: "invoice-test", EventID: hour.eventID, SchemaVersion: 1, CustomerID: customer, SandboxID: "sandbox", Metric: "cpu_seconds", PeriodStart: start, PeriodEnd: start.Add(time.Hour), Units: hour.units}
		if err := transport.InsertBatch(context.Background(), []usage.Event{event}, parseBillingTime(t, received)); err != nil {
			t.Fatal(err)
		}
	}
}

// assertInvoiceExpectation compares literal scenario outputs and then reads the
// exact remaining credit; it never calculates expected financial amounts.
func assertInvoiceExpectation(t testing.TB, store *billing.Store, actual billing.Invoice, want invoiceExpectation) {
	t.Helper()
	var lines []int64
	for _, line := range actual.Lines {
		lines = append(lines, line.AmountCents)
	}
	if actual.Month != want.month || actual.Number != want.number || actual.TotalCents != want.totalCents || !reflect.DeepEqual(lines, want.lineCents) || actual.GrossUsageTicks != want.grossTicks || actual.CreditUsedTicks != want.creditTicks {
		t.Fatalf("invoice=%+v lines=%v want %+v", actual, lines, want)
	}
	credit, err := store.Credit(context.Background(), actual.CustomerID)
	if err != nil {
		t.Fatal(err)
	}
	if credit.BalanceTicks != want.remainingCredit {
		t.Fatalf("credit after %s=%s want %s", actual.Number, credit.BalanceTicks, want.remainingCredit)
	}
}

// TestInvoiceAPI checks empty-month issuance, immutable GET, and invalid input
// through real HTTP routing while keeping each complete scenario isolated.
func TestInvoiceAPI(t *testing.T) {
	cases := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
	}{
		{name: "issue empty month", method: "POST", path: "/customers/acme/invoices", body: `{"month":"2026-10"}`, wantStatus: 200},
		{name: "missing invoice", method: "GET", path: "/customers/acme/invoices/2026-10", wantStatus: 404},
		{name: "invalid month", method: "POST", path: "/customers/acme/invoices", body: `{"month":"2026-13"}`, wantStatus: 422},
		{name: "missing customer", method: "POST", path: "/customers/missing/invoices", body: `{"month":"2026-10"}`, wantStatus: 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := financialRequest(t, financialHandler(t), tc.method, tc.path, tc.body)
			if response.Code != tc.wantStatus {
				t.Fatalf("%s %s(%s): status=%d body=%s want %d", tc.method, tc.path, tc.body, response.Code, response.Body, tc.wantStatus)
			}
		})
	}
}
