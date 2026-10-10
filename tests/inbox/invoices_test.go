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
	"e2b/billing-api/internal/httpapi"
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
// Crossing-month receipts block publication and numbering; an interval ending
// exactly at the UTC month boundary closes October once, including on retry.
// Server-clock boundaries reject unfinished months before any financial effect.
// A receipt inserted before capture but committed afterward stays outside the
// fixed cohort, even with an earlier receipt timestamp and worker processing.
func TestCloseMonth(t *testing.T) {
	cases := []struct {
		name         string
		customer     string
		invoiceTime  string
		creditCents  int64
		purchaseTime string
		october      []measuredHour
		later        []measuredHour
		wantOctober  invoiceExpectation
		wantNovember *invoiceExpectation
	}{
		{name: "Acme October and late November assignment", customer: "acme", invoiceTime: "2026-12-01T00:00:00Z", creditCents: 2500, purchaseTime: "2026-10-05T00:00:00Z",
			october:      []measuredHour{{eventID: "oct-first", start: "2026-10-10T12:00:00Z", units: 100_000_000}, {eventID: "oct-second", start: "2026-10-20T12:00:00Z", units: 200_000_000}},
			later:        []measuredHour{{eventID: "late-oct", start: "2026-10-30T12:00:00Z", units: 50_000_000}, {eventID: "nov", start: "2026-11-03T12:00:00Z", units: 100_000_000}},
			wantOctober:  invoiceExpectation{month: "2026-10", number: "ACME-0001", totalCents: 2000, lineCents: []int64{1200, 2000, -1200}, grossTicks: "1200000000", creditTicks: "1200000000", remainingCredit: "1300000000"},
			wantNovember: &invoiceExpectation{month: "2026-11", number: "ACME-0002", totalCents: 2000, lineCents: []int64{200, 400, 2000, -600}, grossTicks: "600000000", creditTicks: "600000000", remainingCredit: "700000000"}},
		{name: "Cyberdyne October historical prices", customer: "cyberdyne", invoiceTime: "2026-11-01T00:00:00Z",
			october:     []measuredHour{{eventID: "oct-first", start: "2026-10-10T12:00:00Z", units: 123_456_789}, {eventID: "oct-second", start: "2026-10-20T12:00:00Z", units: 200_000_000}},
			wantOctober: invoiceExpectation{month: "2026-10", number: "CYBERDYNE-0001", totalCents: 1817, lineCents: []int64{617, 1200, 0}, grossTicks: "1817283945", creditTicks: "0", remainingCredit: "0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			ctx := context.Background()
			invoiceTime := parseBillingTime(t, tc.invoiceTime)
			store := billing.NewStoreWithClock(pool, func() time.Time { return invoiceTime })
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
	t.Run("UTC month boundary", func(t *testing.T) {
		type closingState struct {
			invoices             int
			closedMonths         int
			frozenGroups         int
			groups               int
			ratings              int
			monthlyUsageRows     int
			creditEntries        int
			cohortReceipts       int
			nextInvoiceNumber    int64
			stateVersion         int64
			remainingCreditTicks string
		}

		cases := []struct {
			name                string
			customer            string
			invoiceTime         string
			start               string
			end                 string
			units               int64
			receiptTime         string
			initialCreditTicks  string
			closingMonths       []string
			wantError           bool
			wantCloseError      error
			wantCompleted       bool
			wantProcessingError string
			wantInvoice         invoiceExpectation
			wantClosingState    closingState
		}{
			{
				name:                "cross-month receipt blocks invoice and retry without financial effects",
				customer:            "acme",
				invoiceTime:         "2026-11-01T00:02:00Z",
				start:               "2026-10-31T23:59:00Z",
				end:                 "2026-11-01T00:01:00Z",
				units:               100_000_000,
				receiptTime:         "2026-11-01T00:02:00Z",
				initialCreditTicks:  "2500000000",
				closingMonths:       []string{"2026-10", "2026-10"},
				wantError:           true,
				wantCloseError:      billing.ErrConflict,
				wantCompleted:       false,
				wantProcessingError: "usage interval must fit within one UTC month",
				wantClosingState: closingState{
					invoices: 0, closedMonths: 0, frozenGroups: 0, groups: 0,
					ratings: 0, monthlyUsageRows: 0, creditEntries: 0, cohortReceipts: 1,
					nextInvoiceNumber: 1, stateVersion: 0, remainingCreditTicks: "2500000000",
				},
			},
			{
				name:                "receipt ending at UTC month boundary closes October exactly once",
				customer:            "acme",
				invoiceTime:         "2026-11-01T00:02:00Z",
				start:               "2026-10-31T23:59:00Z",
				end:                 "2026-11-01T00:00:00Z",
				units:               100_000_000,
				receiptTime:         "2026-11-01T00:02:00Z",
				initialCreditTicks:  "2500000000",
				closingMonths:       []string{"2026-10", "2026-10"},
				wantError:           false,
				wantCloseError:      nil,
				wantCompleted:       true,
				wantProcessingError: "",
				wantInvoice: invoiceExpectation{
					month: "2026-10", number: "ACME-0001", totalCents: 0, lineCents: []int64{400, -400},
					grossTicks: "400000000", creditTicks: "400000000", remainingCredit: "2100000000",
				},
				wantClosingState: closingState{
					invoices: 1, closedMonths: 1, frozenGroups: 1, groups: 1,
					ratings: 1, monthlyUsageRows: 1, creditEntries: 1, cohortReceipts: 1,
					nextInvoiceNumber: 2, stateVersion: 2, remainingCreditTicks: "2100000000",
				},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				pool := billingDatabase(t)
				ctx := context.Background()
				if _, err := pool.Exec(ctx, "UPDATE customer_billing_state SET credit_balance_ticks=$2::numeric WHERE customer_id=$1", tc.customer, tc.initialCreditTicks); err != nil {
					t.Fatalf("Set initial credit for customer %q to %s ticks: %v", tc.customer, tc.initialCreditTicks, err)
				}

				event := usage.Event{
					Source: "closing-month-boundary", EventID: "one", SchemaVersion: 1,
					CustomerID: tc.customer, SandboxID: "sandbox", Metric: "cpu_seconds",
					PeriodStart: parseBillingTime(t, tc.start), PeriodEnd: parseBillingTime(t, tc.end), Units: tc.units,
				}
				if err := inbox.NewPostgres(pool, time.Second).InsertBatch(ctx, []usage.Event{event}, parseBillingTime(t, tc.receiptTime)); err != nil {
					t.Fatalf("Insert closing receipt %+v at %s: %v", event, tc.receiptTime, err)
				}

				invoiceTime := parseBillingTime(t, tc.invoiceTime)
				store := billing.NewStoreWithClock(pool, func() time.Time { return invoiceTime })
				for step, month := range tc.closingMonths {
					invoice, err := store.CloseMonth(ctx, tc.customer, month)
					if (err != nil) != tc.wantError {
						t.Fatalf("CloseMonth(%q, %q) for receipt %+v step %d error = %v; wantError %t", tc.customer, month, event, step+1, err, tc.wantError)
					}
					if !errors.Is(err, tc.wantCloseError) {
						t.Fatalf("CloseMonth(%q, %q) for receipt %+v step %d error = %v; want %v", tc.customer, month, event, step+1, err, tc.wantCloseError)
					}

					if tc.wantError {
						if !reflect.DeepEqual(invoice, billing.Invoice{}) {
							t.Errorf("Failed CloseMonth(%q, %q) step %d invoice = %+v; want an empty result", tc.customer, month, step+1, invoice)
						}
					} else {
						assertInvoiceExpectation(t, store, invoice, tc.wantInvoice)
					}

					var completed bool
					var processingError string
					if err := pool.QueryRow(ctx, "SELECT processed_at IS NOT NULL,COALESCE(processing_error,'') FROM usage_inbox WHERE source=$1 AND event_id=$2", event.Source, event.EventID).Scan(&completed, &processingError); err != nil {
						t.Fatalf("Read closing receipt %+v after step %d: %v", event, step+1, err)
					}
					if completed != tc.wantCompleted || processingError != tc.wantProcessingError {
						t.Errorf("CloseMonth(%q, %q) step %d receipt: completed=%t processing_error=%q; want %t %q", tc.customer, month, step+1, completed, processingError, tc.wantCompleted, tc.wantProcessingError)
					}

					var got closingState
					err = pool.QueryRow(ctx, `SELECT
						(SELECT count(*) FROM invoices),
						(SELECT count(*) FROM closed_billing_months),
						(SELECT count(*) FROM invoiced_usage_groups),
						(SELECT count(*) FROM rated_usage_groups),
						(SELECT count(*) FROM usage_ratings),
						(SELECT count(*) FROM monthly_usage),
						(SELECT count(*) FROM credit_entries),
						(SELECT count(*) FROM invoice_closing_receipts),
						next_invoice_number,state_version,credit_balance_ticks::text
						FROM customer_billing_state WHERE customer_id=$1`, tc.customer).Scan(
						&got.invoices, &got.closedMonths, &got.frozenGroups, &got.groups,
						&got.ratings, &got.monthlyUsageRows, &got.creditEntries, &got.cohortReceipts,
						&got.nextInvoiceNumber, &got.stateVersion, &got.remainingCreditTicks,
					)
					if err != nil {
						t.Fatalf("Read closing state after CloseMonth(%q, %q) step %d: %v", tc.customer, month, step+1, err)
					}
					if got != tc.wantClosingState {
						t.Errorf("CloseMonth(%q, %q) for receipt %+v step %d state = %+v; want %+v", tc.customer, month, event, step+1, got, tc.wantClosingState)
					}
				}
			})
		}
	})

	t.Run("completed calendar month boundary", func(t *testing.T) {
		type financialState struct {
			invoices, closingMonths, closedMonths, frozenGroups, ratedGroups int
			ratings, monthlyUsageRows, creditEntries, cohortReceipts         int
			nextInvoiceNumber, stateVersion                                  int64
			creditTicks, receiptError                                        string
			receiptProcessed                                                 bool
		}

		cases := []struct {
			name, customer, month, serverTime, initialCreditTicks string
			usageStart, usageEnd, receivedAt                      string
			units                                                 int64
			wantError                                             bool
			wantErrorField, wantErrorMessage, wantIssuedAt        string
			wantInvoice                                           invoiceExpectation
			wantState                                             financialState
		}{
			{
				name: "January cannot close on January twentieth", customer: "acme", month: "2027-01", serverTime: "2027-01-20T12:00:00Z",
				initialCreditTicks: "1000000000", usageStart: "2027-01-10T12:00:00Z", usageEnd: "2027-01-10T13:00:00Z", receivedAt: "2027-01-11T00:00:00Z", units: 100_000_000,
				wantError: true, wantErrorField: "month", wantErrorMessage: "Only completed UTC calendar months can be closed.",
				wantState: financialState{invoices: 0, closingMonths: 0, closedMonths: 0, frozenGroups: 0, ratedGroups: 0, ratings: 0, monthlyUsageRows: 0, creditEntries: 0, cohortReceipts: 0,
					nextInvoiceNumber: 1, stateVersion: 0, creditTicks: "1000000000", receiptProcessed: false, receiptError: ""},
			},
			{
				name: "last microsecond of January is still too early", customer: "acme", month: "2027-01", serverTime: "2027-01-31T23:59:59.999999Z",
				initialCreditTicks: "1000000000", usageStart: "2027-01-10T12:00:00Z", usageEnd: "2027-01-10T13:00:00Z", receivedAt: "2027-01-11T00:00:00Z", units: 100_000_000,
				wantError: true, wantErrorField: "month", wantErrorMessage: "Only completed UTC calendar months can be closed.",
				wantState: financialState{invoices: 0, closingMonths: 0, closedMonths: 0, frozenGroups: 0, ratedGroups: 0, ratings: 0, monthlyUsageRows: 0, creditEntries: 0, cohortReceipts: 0,
					nextInvoiceNumber: 1, stateVersion: 0, creditTicks: "1000000000", receiptProcessed: false, receiptError: ""},
			},
			{
				name: "February local date cannot bypass January UTC boundary", customer: "acme", month: "2027-01", serverTime: "2027-02-01T07:59:59.999999+08:00",
				initialCreditTicks: "1000000000", usageStart: "2027-01-10T12:00:00Z", usageEnd: "2027-01-10T13:00:00Z", receivedAt: "2027-01-11T00:00:00Z", units: 100_000_000,
				wantError: true, wantErrorField: "month", wantErrorMessage: "Only completed UTC calendar months can be closed.",
				wantState: financialState{invoices: 0, closingMonths: 0, closedMonths: 0, frozenGroups: 0, ratedGroups: 0, ratings: 0, monthlyUsageRows: 0, creditEntries: 0, cohortReceipts: 0,
					nextInvoiceNumber: 1, stateVersion: 0, creditTicks: "1000000000", receiptProcessed: false, receiptError: ""},
			},
			{
				name: "future February cannot close in January", customer: "acme", month: "2027-02", serverTime: "2027-01-20T12:00:00Z",
				initialCreditTicks: "1000000000", usageStart: "2027-01-10T12:00:00Z", usageEnd: "2027-01-10T13:00:00Z", receivedAt: "2027-01-11T00:00:00Z", units: 100_000_000,
				wantError: true, wantErrorField: "month", wantErrorMessage: "Only completed UTC calendar months can be closed.",
				wantState: financialState{invoices: 0, closingMonths: 0, closedMonths: 0, frozenGroups: 0, ratedGroups: 0, ratings: 0, monthlyUsageRows: 0, creditEntries: 0, cohortReceipts: 0,
					nextInvoiceNumber: 1, stateVersion: 0, creditTicks: "1000000000", receiptProcessed: false, receiptError: ""},
			},
			{
				name: "January closes exactly at February UTC start", customer: "acme", month: "2027-01", serverTime: "2027-02-01T00:00:00Z",
				initialCreditTicks: "1000000000", usageStart: "2027-01-10T12:00:00Z", usageEnd: "2027-01-10T13:00:00Z", receivedAt: "2027-01-11T00:00:00Z", units: 100_000_000,
				wantError: false, wantIssuedAt: "2027-02-01T00:00:00Z",
				wantInvoice: invoiceExpectation{month: "2027-01", number: "ACME-0001", totalCents: 0, lineCents: []int64{400, -400}, grossTicks: "400000000", creditTicks: "400000000", remainingCredit: "600000000"},
				wantState: financialState{invoices: 1, closingMonths: 1, closedMonths: 1, frozenGroups: 1, ratedGroups: 1, ratings: 1, monthlyUsageRows: 1, creditEntries: 1, cohortReceipts: 1,
					nextInvoiceNumber: 2, stateVersion: 2, creditTicks: "600000000", receiptProcessed: true, receiptError: ""},
			},
			{
				name: "offset clock closes January at the same UTC instant", customer: "acme", month: "2027-01", serverTime: "2027-02-01T08:00:00+08:00",
				initialCreditTicks: "1000000000", usageStart: "2027-01-10T12:00:00Z", usageEnd: "2027-01-10T13:00:00Z", receivedAt: "2027-01-11T00:00:00Z", units: 100_000_000,
				wantError: false, wantIssuedAt: "2027-02-01T00:00:00Z",
				wantInvoice: invoiceExpectation{month: "2027-01", number: "ACME-0001", totalCents: 0, lineCents: []int64{400, -400}, grossTicks: "400000000", creditTicks: "400000000", remainingCredit: "600000000"},
				wantState: financialState{invoices: 1, closingMonths: 1, closedMonths: 1, frozenGroups: 1, ratedGroups: 1, ratings: 1, monthlyUsageRows: 1, creditEntries: 1, cohortReceipts: 1,
					nextInvoiceNumber: 2, stateVersion: 2, creditTicks: "600000000", receiptProcessed: true, receiptError: ""},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				pool := billingDatabase(t)
				ctx := context.Background()
				serverTime := parseBillingTime(t, tc.serverTime)
				store := billing.NewStoreWithClock(pool, func() time.Time { return serverTime })
				if _, err := pool.Exec(ctx, "UPDATE customer_billing_state SET credit_balance_ticks=$2::numeric WHERE customer_id=$1", tc.customer, tc.initialCreditTicks); err != nil {
					t.Fatalf("Set initial credit for %q to %s ticks: %v", tc.customer, tc.initialCreditTicks, err)
				}

				event := usage.Event{Source: "closing-clock-boundary", EventID: "one", SchemaVersion: 1, CustomerID: tc.customer, SandboxID: "sandbox", Metric: "cpu_seconds",
					PeriodStart: parseBillingTime(t, tc.usageStart), PeriodEnd: parseBillingTime(t, tc.usageEnd), Units: tc.units}
				if err := inbox.NewPostgres(pool, time.Second).InsertBatch(ctx, []usage.Event{event}, parseBillingTime(t, tc.receivedAt)); err != nil {
					t.Fatalf("Insert pending receipt %+v at %s: %v", event, tc.receivedAt, err)
				}

				var firstInvoice billing.Invoice
				for attempt := range 2 {
					invoice, err := store.CloseMonth(ctx, tc.customer, tc.month)
					if (err != nil) != tc.wantError {
						t.Fatalf("CloseMonth(%q, %q) at %s attempt %d error = %v; wantError %t", tc.customer, tc.month, tc.serverTime, attempt+1, err, tc.wantError)
					}
					if tc.wantError {
						var validationError *billing.ValidationError
						if !errors.As(err, &validationError) || validationError.Field != tc.wantErrorField || validationError.Message != tc.wantErrorMessage {
							t.Fatalf("CloseMonth(%q, %q) at %s error = %v; want validation field=%q message=%q", tc.customer, tc.month, tc.serverTime, err, tc.wantErrorField, tc.wantErrorMessage)
						}

						if !reflect.DeepEqual(invoice, billing.Invoice{}) {
							t.Errorf("Rejected CloseMonth(%q, %q) at %s invoice = %+v; want empty result", tc.customer, tc.month, tc.serverTime, invoice)
						}
					} else {
						assertInvoiceExpectation(t, store, invoice, tc.wantInvoice)
						if got := invoice.IssuedAt.Format(time.RFC3339Nano); got != tc.wantIssuedAt {
							t.Errorf("CloseMonth(%q, %q) at %s issued_at = %s; want %s", tc.customer, tc.month, tc.serverTime, got, tc.wantIssuedAt)
						}
						if attempt == 0 {
							firstInvoice = invoice
						} else if !reflect.DeepEqual(invoice, firstInvoice) {
							t.Errorf("Retry CloseMonth(%q, %q) at %s invoice = %+v; want original snapshot %+v", tc.customer, tc.month, tc.serverTime, invoice, firstInvoice)
						}
					}

					var got financialState
					err = pool.QueryRow(ctx, `SELECT
						(SELECT count(*) FROM invoices), (SELECT count(*) FROM invoice_closings),
						(SELECT count(*) FROM closed_billing_months), (SELECT count(*) FROM invoiced_usage_groups),
						(SELECT count(*) FROM rated_usage_groups), (SELECT count(*) FROM usage_ratings),
						(SELECT count(*) FROM monthly_usage), (SELECT count(*) FROM credit_entries),
						(SELECT count(*) FROM invoice_closing_receipts),
						next_invoice_number,state_version,credit_balance_ticks::text,
						(SELECT processed_at IS NOT NULL FROM usage_inbox WHERE source=$2 AND event_id=$3),
						(SELECT COALESCE(processing_error,'') FROM usage_inbox WHERE source=$2 AND event_id=$3)
						FROM customer_billing_state WHERE customer_id=$1`, tc.customer, event.Source, event.EventID).Scan(
						&got.invoices, &got.closingMonths, &got.closedMonths, &got.frozenGroups, &got.ratedGroups,
						&got.ratings, &got.monthlyUsageRows, &got.creditEntries, &got.cohortReceipts,
						&got.nextInvoiceNumber, &got.stateVersion, &got.creditTicks, &got.receiptProcessed, &got.receiptError,
					)
					if err != nil {
						t.Fatalf("Read state after CloseMonth(%q, %q) at %s attempt %d: %v", tc.customer, tc.month, tc.serverTime, attempt+1, err)
					}

					if got != tc.wantState {
						t.Errorf("CloseMonth(%q, %q) at %s attempt %d state = %+v; want %+v", tc.customer, tc.month, tc.serverTime, attempt+1, got, tc.wantState)
					}
				}
			})
		}
	})

	t.Run("concurrent closing allocates one number", func(t *testing.T) {
		scenario := struct {
			workers     int
			month       string
			invoiceTime string
			wantNumber  string
			wantCount   int
		}{workers: 8, month: "2026-10", invoiceTime: "2026-11-01T00:00:00Z", wantNumber: "ACME-0001", wantCount: 1}
		pool := billingDatabase(t)
		invoiceTime := parseBillingTime(t, scenario.invoiceTime)
		store := billing.NewStoreWithClock(pool, func() time.Time { return invoiceTime })
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
			invoiceTime         string
			originalUnits       int64
			laterUnits          int64
			wantFirstError      error
			wantOctoberTotal    int64
			wantNovemberTotal   int64
			wantCohort          int
			wantProcessingError string
			wantLog             missingPriceLog
		}{
			invoiceTime: "2026-12-01T00:00:00Z", originalUnits: 100_000_000, laterUnits: 100_000_000,
			wantFirstError: billing.ErrConflict, wantOctoberTotal: 400, wantNovemberTotal: 400, wantCohort: 1,
			wantProcessingError: "P0 missing_valid_price: no valid price for customer acme and metric unpriced at 2026-10-10T12:00:00Z",
			wantLog:             missingPriceLog{Level: "ERROR", Priority: "P0", ErrorCode: "missing_valid_price", Source: "cohort", EventID: "original", CustomerID: "acme", Metric: "unpriced", PeriodStart: "2026-10-10T12:00:00Z"},
		}
		cases := []struct {
			name                string
			insertBeforeCapture bool
			wantWorkerProcessed bool
			wantLaterMonth      string
		}{
			{name: "receipt inserted after capture", insertBeforeCapture: false, wantWorkerProcessed: true, wantLaterMonth: "2026-11"},
			{name: "receipt inserted before capture and committed afterward", insertBeforeCapture: true, wantWorkerProcessed: true, wantLaterMonth: "2026-11"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				pool := billingDatabase(t)
				ctx := context.Background()
				logs := captureBillingLogs(t)
				invoiceTime := parseBillingTime(t, scenario.invoiceTime)
				store := billing.NewStoreWithClock(pool, func() time.Time { return invoiceTime })
				transport := inbox.NewPostgres(pool, time.Second)
				if _, err := pool.Exec(ctx, "INSERT INTO metrics VALUES ('unpriced')"); err != nil {
					t.Fatal(err)
				}
				event := usage.Event{Source: "cohort", EventID: "original", SchemaVersion: 1, CustomerID: "acme", SandboxID: "sandbox", Metric: "unpriced", PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z"), Units: scenario.originalUnits}
				if err := transport.InsertBatch(ctx, []usage.Event{event}, event.PeriodEnd); err != nil {
					t.Fatal(err)
				}

				later := usage.Event{Source: "cohort", EventID: "later", SchemaVersion: 1,
					CustomerID: "acme", SandboxID: "sandbox", Metric: "cpu_seconds",
					PeriodStart: parseBillingTime(t, "2026-10-10T12:00:00Z"), PeriodEnd: parseBillingTime(t, "2026-10-10T13:00:00Z"), Units: scenario.laterUnits}
				pendingReceipt, err := pool.Begin(ctx)
				if err != nil {
					t.Fatalf("Begin receipt insertion before cohort capture: %v", err)
				}
				defer func() { _ = pendingReceipt.Rollback(ctx) }()

				// Keep the receipt invisible to capture until its transaction commits.
				if tc.insertBeforeCapture {
					if _, err := pendingReceipt.Exec(ctx, `INSERT INTO usage_inbox
            (source,event_id,schema_version,customer_id,sandbox_id,metric,period_start,period_end,units,received_at)
            VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
						later.Source, later.EventID, later.SchemaVersion, later.CustomerID, later.SandboxID,
						later.Metric, later.PeriodStart, later.PeriodEnd, later.Units, event.PeriodStart); err != nil {
						t.Fatalf("Insert uncommitted receipt %+v before cohort capture: %v", later, err)
					}
				}

				_, err = store.CloseMonth(ctx, "acme", "2026-10")
				if !errors.Is(err, scenario.wantFirstError) {
					t.Fatalf("unpriced cohort error=%v want %v", err, scenario.wantFirstError)
				}
				var processingError string
				if err := pool.QueryRow(ctx, "SELECT COALESCE(processing_error,'') FROM usage_inbox WHERE source=$1 AND event_id=$2", event.Source, event.EventID).Scan(&processingError); err != nil {
					t.Fatalf("Read P0 cohort processing error: %v", err)
				}
				if processingError != scenario.wantProcessingError {
					t.Errorf("Unpriced cohort processing_error=%q; want %q", processingError, scenario.wantProcessingError)
				}
				if got := readMissingPriceLogs(t, logs); !reflect.DeepEqual(got, []missingPriceLog{scenario.wantLog}) {
					t.Errorf("Closing catalog incident log=%+v; want %+v", got, scenario.wantLog)
				}
				// The earlier receipt timestamp cannot bypass the committed cohort cut.
				if err := pendingReceipt.Commit(ctx); err != nil {
					t.Fatalf("Commit receipt %+v after cohort capture: %v", later, err)
				}
				if !tc.insertBeforeCapture {
					if err := transport.InsertBatch(ctx, []usage.Event{later}, event.PeriodStart); err != nil {
						t.Fatalf("Insert receipt %+v after cohort capture: %v", later, err)
					}
				}

				processed, err := store.ProcessBatch(ctx)
				if err != nil {
					t.Fatalf("ProcessBatch for receipt %+v outside the closing cohort: %v", later, err)
				}

				if processed != tc.wantWorkerProcessed {
					t.Errorf("ProcessBatch for receipt %+v processed=%t; want %t", later, processed, tc.wantWorkerProcessed)
				}
				var laterMonth string
				if err := pool.QueryRow(ctx, `SELECT to_char(g.billing_month,'YYYY-MM')
			FROM usage_ratings r JOIN rated_usage_groups g USING (group_id)
			WHERE r.source=$1 AND r.event_id=$2`, later.Source, later.EventID).Scan(&laterMonth); err != nil {
					t.Fatalf("Read worker routing for receipt %+v: %v", later, err)
				}

				if laterMonth != tc.wantLaterMonth {
					t.Errorf("Worker routed receipt %+v to %s; want %s", later, laterMonth, tc.wantLaterMonth)
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
				if got := readMissingPriceLogs(t, logs); !reflect.DeepEqual(got, []missingPriceLog{scenario.wantLog}) {
					t.Errorf("Closing recovery reports=%+v; want only original %+v", got, scenario.wantLog)
				}
			})
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
// Unfinished months and day-specific commands fail without financial effects;
// the exact UTC start of the next month permits a new immutable invoice.
func TestInvoiceAPI(t *testing.T) {
	type invoiceAPIState struct {
		invoices          int
		closingMonths     int
		closedMonths      int
		nextInvoiceNumber int64
		stateVersion      int64
		creditTicks       string
	}

	cases := []struct {
		name           string
		method         string
		path           string
		body           string
		serverTime     string
		wantStatus     int
		wantError      bool
		wantErrorCode  string
		wantErrorField string
		wantState      invoiceAPIState
	}{
		{
			name: "issue empty month", method: "POST", path: "/customers/acme/invoices", body: `{"month":"2026-10"}`, serverTime: "2026-11-01T00:00:00Z",
			wantStatus: 200, wantError: false,
			wantState: invoiceAPIState{invoices: 1, closingMonths: 1, closedMonths: 1, nextInvoiceNumber: 2, stateVersion: 1, creditTicks: "0"},
		},
		{
			name: "missing invoice", method: "GET", path: "/customers/acme/invoices/2026-10", serverTime: "2026-11-01T00:00:00Z",
			wantStatus: 404, wantError: true, wantErrorCode: "not_found", wantErrorField: "",
			wantState: invoiceAPIState{invoices: 0, closingMonths: 0, closedMonths: 0, nextInvoiceNumber: 1, stateVersion: 0, creditTicks: "0"},
		},
		{
			name: "invalid month", method: "POST", path: "/customers/acme/invoices", body: `{"month":"2026-13"}`, serverTime: "2026-11-01T00:00:00Z",
			wantStatus: 422, wantError: true, wantErrorCode: "invalid_command", wantErrorField: "month",
			wantState: invoiceAPIState{invoices: 0, closingMonths: 0, closedMonths: 0, nextInvoiceNumber: 1, stateVersion: 0, creditTicks: "0"},
		},
		{
			name: "missing customer", method: "POST", path: "/customers/missing/invoices", body: `{"month":"2026-10"}`, serverTime: "2026-11-01T00:00:00Z",
			wantStatus: 404, wantError: true, wantErrorCode: "not_found", wantErrorField: "",
			wantState: invoiceAPIState{invoices: 0, closingMonths: 0, closedMonths: 0, nextInvoiceNumber: 1, stateVersion: 0, creditTicks: "0"},
		},
		{
			name: "future month is rejected with the real server clock", method: "POST", path: "/customers/acme/invoices", body: `{"month":"9999-11"}`, serverTime: "",
			wantStatus: 422, wantError: true, wantErrorCode: "invalid_command", wantErrorField: "month",
			wantState: invoiceAPIState{invoices: 0, closingMonths: 0, closedMonths: 0, nextInvoiceNumber: 1, stateVersion: 0, creditTicks: "0"},
		},
		{
			name: "January is rejected on January twentieth", method: "POST", path: "/customers/acme/invoices", body: `{"month":"2027-01"}`, serverTime: "2027-01-20T12:00:00Z",
			wantStatus: 422, wantError: true, wantErrorCode: "invalid_command", wantErrorField: "month",
			wantState: invoiceAPIState{invoices: 0, closingMonths: 0, closedMonths: 0, nextInvoiceNumber: 1, stateVersion: 0, creditTicks: "0"},
		},
		{
			name: "January is rejected one microsecond before February", method: "POST", path: "/customers/acme/invoices", body: `{"month":"2027-01"}`, serverTime: "2027-01-31T23:59:59.999999Z",
			wantStatus: 422, wantError: true, wantErrorCode: "invalid_command", wantErrorField: "month",
			wantState: invoiceAPIState{invoices: 0, closingMonths: 0, closedMonths: 0, nextInvoiceNumber: 1, stateVersion: 0, creditTicks: "0"},
		},
		{
			name: "January is accepted exactly at February UTC start", method: "POST", path: "/customers/acme/invoices", body: `{"month":"2027-01"}`, serverTime: "2027-02-01T00:00:00Z",
			wantStatus: 200, wantError: false,
			wantState: invoiceAPIState{invoices: 1, closingMonths: 1, closedMonths: 1, nextInvoiceNumber: 2, stateVersion: 1, creditTicks: "0"},
		},
		{
			name: "month cannot contain a closing day", method: "POST", path: "/customers/acme/invoices", body: `{"month":"2027-01-20"}`, serverTime: "2027-02-01T00:00:00Z",
			wantStatus: 422, wantError: true, wantErrorCode: "invalid_command", wantErrorField: "month",
			wantState: invoiceAPIState{invoices: 0, closingMonths: 0, closedMonths: 0, nextInvoiceNumber: 1, stateVersion: 0, creditTicks: "0"},
		},
		{
			name: "explicit closing timestamp is unsupported", method: "POST", path: "/customers/acme/invoices", body: `{"month":"2027-01","closed_at":"2027-01-20T00:00:00Z"}`, serverTime: "2027-02-01T00:00:00Z",
			wantStatus: 400, wantError: true, wantErrorCode: "invalid_json", wantErrorField: "",
			wantState: invoiceAPIState{invoices: 0, closingMonths: 0, closedMonths: 0, nextInvoiceNumber: 1, stateVersion: 0, creditTicks: "0"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := billingDatabase(t)
			store := billing.NewStore(pool)
			if tc.serverTime != "" {
				serverTime := parseBillingTime(t, tc.serverTime)
				store = billing.NewStoreWithClock(pool, func() time.Time { return serverTime })
			}
			handler := httpapi.NewHandler(inbox.NewPostgres(pool, time.Second), 5*time.Second, 8, store)

			response := financialRequest(t, handler, tc.method, tc.path, tc.body)

			if response.Code != tc.wantStatus {
				t.Fatalf("%s %s(%s): status=%d body=%s want %d", tc.method, tc.path, tc.body, response.Code, response.Body, tc.wantStatus)
			}

			if tc.wantError {
				var actual struct {
					Error struct {
						Code  string `json:"code"`
						Field string `json:"field"`
					} `json:"error"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
					t.Fatalf("Decode %s %s(%s) error response: %v", tc.method, tc.path, tc.body, err)
				}

				if actual.Error.Code != tc.wantErrorCode || actual.Error.Field != tc.wantErrorField {
					t.Errorf("%s %s(%s): error=%+v; want code=%q field=%q", tc.method, tc.path, tc.body, actual.Error, tc.wantErrorCode, tc.wantErrorField)
				}
			}

			var actual invoiceAPIState
			err := pool.QueryRow(context.Background(), `SELECT
                (SELECT count(*) FROM invoices),
                (SELECT count(*) FROM invoice_closings),
                (SELECT count(*) FROM closed_billing_months),
                next_invoice_number,state_version,credit_balance_ticks::text
                FROM customer_billing_state WHERE customer_id='acme'`).Scan(
				&actual.invoices, &actual.closingMonths, &actual.closedMonths,
				&actual.nextInvoiceNumber, &actual.stateVersion, &actual.creditTicks,
			)
			if err != nil {
				t.Fatalf("Read state after %s %s(%s): %v", tc.method, tc.path, tc.body, err)
			}

			if actual != tc.wantState {
				t.Errorf("%s %s(%s): state=%+v; want %+v", tc.method, tc.path, tc.body, actual, tc.wantState)
			}
		})
	}
}
