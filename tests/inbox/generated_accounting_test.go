//go:build integration

package inbox_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/postgres"
	"e2b/billing-api/internal/usage"
	"github.com/jackc/pgx/v5/pgxpool"
)

type generatedInput struct {
	name, kind, key, usageMonth, receivedMonth string
	cents, units                               int64
	limit                                      *int64
}
type referenceGroup struct {
	usageMonth, billingMonth string
	units, gross, credit     int64
}
type referenceAccount struct {
	balance, version, nextNumber int64
	limit                        *int64
	grantKeys                    map[string]bool
	receipts                     map[string]bool
	groups                       map[string]referenceGroup
	grossByMonth                 map[string]int64
	closed                       map[string]bool
}
type generatedStep struct {
	input generatedInput
	want  referenceAccount
}

// testGeneratedAccountingSequences builds all inputs and independent expected
// states before execution. Each seed owns a schema; every step checks literal
// rules through real persistence, including late usage after October publication.
func testGeneratedAccountingSequences(t *testing.T) {
	cases := []struct {
		name                   string
		seed                   int64
		stepsPerMonth          int
		units                  []int64
		grantCents             []int64
		limits                 []*int64
		priceMicrocentsPerUnit int64
	}{
		{name: "fractional charges and exhausted credit", seed: 7, stepsPerMonth: 24, units: []int64{1, 124_999, 125_000, 250_000, 1_000_000}, grantCents: []int64{1, 2, 5}, limits: []*int64{nil, limitValue(0), limitValue(1), limitValue(5)}, priceMicrocentsPerUnit: 4},
		{name: "larger charges and recurring retries", seed: 42, stepsPerMonth: 24, units: []int64{0, 1, 125_001, 5_000_000, 10_000_000}, grantCents: []int64{1, 10, 50}, limits: []*int64{nil, limitValue(0), limitValue(25), limitValue(100)}, priceMicrocentsPerUnit: 4},
		{name: "many subcent receipts share a group", seed: 1901, stepsPerMonth: 24, units: []int64{1, 2, 3, 124_999, 125_000}, grantCents: []int64{1, 2}, limits: []*int64{nil, limitValue(0), limitValue(1)}, priceMicrocentsPerUnit: 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inputs := generatedAccountingInputs(tc.seed, tc.stepsPerMonth, tc.units, tc.grantCents, tc.limits)
			steps := referenceAccountingSteps(inputs, tc.priceMicrocentsPerUnit)
			pool := billingDatabase(t)
			clock := parseBillingTime(t, "2027-01-01T00:00:00Z")
			store := newPostgresStore(t, pool, postgres.Config{Now: func() time.Time { return clock }})
			snapshots := map[string]billing.Invoice{}

			for _, step := range steps {
				t.Run(step.input.name, func(t *testing.T) {
					executeGeneratedInput(t, store, step.input)

					assertReferenceAccount(t, pool, store, step.want, step.input)
					assertAccountingInvariants(t, pool)
					assertReferenceInvoices(t, store, step.want, snapshots)
				})
			}
		})
	}
}

// generatedAccountingInputs uses bounded workload choices and stable seeds. It
// includes grants, usage, old identities, nullable limits and chronological closes;
// November may contain original October usage without assuming order independence.
func generatedAccountingInputs(seed int64, count int, units, grants []int64, limits []*int64) []generatedInput {
	random := rand.New(rand.NewSource(seed))
	var inputs, replayable []generatedInput
	for _, month := range []string{"2026-10", "2026-11"} {
		for index := 0; index < count; index++ {
			input := generatedInput{key: fmt.Sprintf("%s-%d", month, index), receivedMonth: month}
			switch random.Intn(4) {
			case 0:
				input.kind, input.cents = "grant", grants[random.Intn(len(grants))]
			case 1:
				input.kind, input.units, input.usageMonth = "usage", units[random.Intn(len(units))], month
				if month == "2026-11" && random.Intn(2) == 0 {
					input.usageMonth = "2026-10"
				}
			case 2:
				input.kind, input.limit = "limit", limits[random.Intn(len(limits))]
			case 3:
				if len(replayable) > 0 {
					input = replayable[random.Intn(len(replayable))]
				} else {
					input.kind, input.cents = "grant", 1
				}
			}
			input.name = fmt.Sprintf("%s step %02d %s %s", month, index, input.kind, input.key)
			inputs = append(inputs, input)
			if input.kind == "grant" || input.kind == "usage" {
				replayable = append(replayable, input)
			}
		}
		inputs = append(inputs, generatedInput{name: month + " close", kind: "close", usageMonth: month})
		inputs = append(inputs, generatedInput{name: month + " immutable closing retry", kind: "close", usageMonth: month})
	}
	return inputs
}

// referenceAccountingSteps computes expected states without any production
// calculator, price selector, allocation, routing or invoice helper. Expected
// snapshots are copied before real operations can mutate their private database.
func referenceAccountingSteps(inputs []generatedInput, price int64) []generatedStep {
	model := referenceAccount{nextNumber: 1, grantKeys: map[string]bool{}, receipts: map[string]bool{}, groups: map[string]referenceGroup{}, grossByMonth: map[string]int64{}, closed: map[string]bool{}}
	var steps []generatedStep
	for _, input := range inputs {
		switch input.kind {
		case "grant":
			if !model.grantKeys[input.key] {
				model.grantKeys[input.key] = true
				model.balance += input.cents * 1_000_000
				model.version++
			}
		case "limit":
			model.limit = input.limit
			model.version++
		case "usage":
			if !model.receipts[input.key] {
				charge := new(big.Int).Mul(big.NewInt(input.units), big.NewInt(price)).Int64()
				used := min(charge, model.balance)
				month := input.usageMonth
				if model.closed[month] {
					month = input.receivedMonth
				}
				key := input.usageMonth + "/" + month
				group := model.groups[key]
				group.usageMonth, group.billingMonth = input.usageMonth, month
				group.units += input.units
				group.gross += charge
				group.credit += used
				model.groups[key] = group
				model.grossByMonth[input.usageMonth] += charge
				model.balance -= used
				model.version++
				model.receipts[input.key] = true
			}
		case "close":
			if !model.closed[input.usageMonth] {
				model.closed[input.usageMonth] = true
				model.version++
				model.nextNumber++
			}
		}
		steps = append(steps, generatedStep{input: input, want: copyReferenceAccount(model)})
	}
	return steps
}

// copyReferenceAccount separates each expected snapshot from later reference
// steps; scenario expectations never alias a mutable map used during generation.
func copyReferenceAccount(model referenceAccount) referenceAccount {
	copy := model
	copy.grantKeys = map[string]bool{}
	copy.receipts = map[string]bool{}
	copy.groups = map[string]referenceGroup{}
	copy.grossByMonth = map[string]int64{}
	copy.closed = map[string]bool{}
	for key, value := range model.grantKeys {
		copy.grantKeys[key] = value
	}
	for key, value := range model.receipts {
		copy.receipts[key] = value
	}
	for key, value := range model.groups {
		copy.groups[key] = value
	}
	for key, value := range model.grossByMonth {
		copy.grossByMonth[key] = value
	}
	for key, value := range model.closed {
		copy.closed[key] = value
	}
	return copy
}

// executeGeneratedInput supplies declared inputs through the real store, then
// drains exactly one new receipt or an empty retry queue without background work.
func executeGeneratedInput(t *testing.T, store *postgres.Store, input generatedInput) {
	t.Helper()
	ctx := context.Background()
	switch input.kind {
	case "grant":
		if err := store.GrantCredit(ctx, "acme", billing.CreditGrant{IdempotencyKey: input.key, AmountCents: input.cents}); err != nil {
			t.Fatalf("GrantCredit(%+v): %v; want nil", input, err)
		}
	case "limit":
		if err := store.SetSpendLimit(ctx, "acme", billing.SpendLimitChange{IdempotencyKey: input.key, LimitCents: input.limit}); err != nil {
			t.Fatalf("SetSpendLimit(%+v): %v; want nil", input, err)
		}
	case "usage":
		start := parseBillingTime(t, input.usageMonth+"-10T12:00:00Z")
		event := usage.Event{Source: "generated", EventID: input.key, SchemaVersion: 1, CustomerID: "acme", SandboxID: "sandbox", Metric: "cpu_seconds", PeriodStart: start, PeriodEnd: start.Add(time.Hour), Units: input.units}
		if err := store.InsertBatch(ctx, []usage.Event{event}, parseBillingTime(t, input.receivedMonth+"-11T00:00:00Z")); err != nil {
			t.Fatalf("InsertBatch(%+v): %v; want nil", input, err)
		}
		if _, err := store.ProcessBatch(ctx); err != nil {
			t.Fatalf("ProcessBatch(%+v): %v; want nil", input, err)
		}
		worked, err := store.ProcessBatch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if worked {
			t.Fatalf("Retry queue after %+v: worked=true; want false", input)
		}
	case "close":
		if _, err := store.CloseMonth(ctx, "acme", input.usageMonth); err != nil {
			t.Fatalf("CloseMonth(%+v): %v; want nil", input, err)
		}
	}
}

// assertReferenceAccount compares the reference's balance, version, limit,
// identities, group totals and original-month gross to committed database values.
func assertReferenceAccount(t *testing.T, pool *pgxpool.Pool, store *postgres.Store, want referenceAccount, input generatedInput) {
	t.Helper()
	ctx := context.Background()
	var balance string
	var version, next int64
	var limit *int64
	var receipts, ratings, grants int
	err := pool.QueryRow(ctx, `SELECT credit_balance_microcents::text,state_version,next_invoice_number,spend_limit_cents,
  (SELECT count(*) FROM usage_inbox),(SELECT count(*) FROM usage_ratings),
  (SELECT count(*) FROM credit_entries WHERE group_id IS NULL) FROM customer_billing_state WHERE customer_id='acme'`).Scan(&balance, &version, &next, &limit, &receipts, &ratings, &grants)
	if err != nil {
		t.Fatal(err)
	}
	if balance != fmt.Sprint(want.balance) || version != want.version || next != want.nextNumber || !reflect.DeepEqual(limit, want.limit) || receipts != len(want.receipts) || ratings != len(want.receipts) || grants != len(want.grantKeys) {
		t.Fatalf("After %+v: balance=%s version=%d next=%d limit=%v receipts=%d ratings=%d grants=%d; want %+v", input, balance, version, next, limit, receipts, ratings, grants, want)
	}
	rows, err := pool.Query(ctx, `SELECT to_char(usage_month,'YYYY-MM'),to_char(billing_month,'YYYY-MM'),total_units::bigint,exact_charge_microcents::bigint,allocated_credit_microcents::bigint,price_version_id,booked_charge_cents FROM rated_usage_groups`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]referenceGroup{}
	for rows.Next() {
		var group referenceGroup
		var price string
		var cents int64
		if err := rows.Scan(&group.usageMonth, &group.billingMonth, &group.units, &group.gross, &group.credit, &price, &cents); err != nil {
			t.Fatal(err)
		}
		if price != "cpu-acme-2026-10-01" || cents != referenceRoundedCents(group.gross) {
			t.Fatalf("After %+v group=%+v: price=%s cents=%d; want cpu-acme-2026-10-01 cents=%d", input, group, price, cents, referenceRoundedCents(group.gross))
		}
		got[group.usageMonth+"/"+group.billingMonth] = group
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want.groups) {
		t.Fatalf("After %+v groups=%+v; want %+v", input, got, want.groups)
	}
	for _, month := range []string{"2026-10", "2026-11"} {
		var gross int64
		if err := pool.QueryRow(ctx, `SELECT COALESCE((SELECT gross_charge_microcents::bigint FROM monthly_usage WHERE customer_id='acme' AND usage_month=$1::date),0)`, month+"-01").Scan(&gross); err != nil {
			t.Fatal(err)
		}
		status, err := store.MonthlyLimitStatus(ctx, "acme", month)
		if err != nil {
			t.Fatal(err)
		}
		wantReached := want.limit != nil && want.grossByMonth[month] >= *want.limit*1_000_000
		if status.Month != month || status.GrossChargeMicrocents != fmt.Sprint(want.grossByMonth[month]) || status.LimitReached != wantReached || status.StateVersion != want.version || status.PendingEvents != 0 || status.ProcessingErrors != 0 {
			t.Fatalf("After %+v status=%+v; want month=%s gross=%d reached=%t version=%d pending=0 errors=0", input, status, month, want.grossByMonth[month], wantReached, want.version)
		}
		if gross != want.grossByMonth[month] {
			t.Fatalf("After %+v month=%s gross=%d; want %d", input, month, gross, want.grossByMonth[month])
		}
	}
}

// referenceRoundedCents expresses half-up rounding with an exact rational
// independent of accounting.Amount and any production presentation function.
func referenceRoundedCents(microcents int64) int64 {
	shifted := new(big.Rat).Add(new(big.Rat).SetFrac(big.NewInt(microcents), big.NewInt(1_000_000)), big.NewRat(1, 2))
	return new(big.Int).Quo(shifted.Num(), shifted.Denom()).Int64()
}

// assertAccountingInvariants requires balanced ledger/account and group debits,
// nonnegative bounded credit, one rating per completed receipt and exact group
// units. Each query states a violated rule and requires zero violating rows.
func assertAccountingInvariants(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	checks := []struct {
		name, sql      string
		wantViolations int
	}{
		{name: "ledger equals balance", sql: `SELECT count(*) FROM customer_billing_state a WHERE credit_balance_microcents<>COALESCE((SELECT sum(amount_microcents) FROM credit_entries e WHERE e.customer_id=a.customer_id),0)`, wantViolations: 0},
		{name: "balance cannot be negative", sql: `SELECT count(*) FROM customer_billing_state WHERE credit_balance_microcents<0`, wantViolations: 0},
		{name: "allocated credit is bounded", sql: `SELECT count(*) FROM rated_usage_groups WHERE allocated_credit_microcents<0 OR allocated_credit_microcents>exact_charge_microcents`, wantViolations: 0},
		{name: "one rating per completed receipt", sql: `SELECT count(*) FROM usage_inbox i WHERE processed_at IS NOT NULL AND (SELECT count(*) FROM usage_ratings r WHERE r.source=i.source AND r.event_id=i.event_id)<>1`, wantViolations: 0},
		{name: "group units equal rated receipts", sql: `SELECT count(*) FROM rated_usage_groups g WHERE total_units<>COALESCE((SELECT sum(i.units) FROM usage_ratings r JOIN usage_inbox i USING(source,event_id) WHERE r.group_id=g.group_id),0)`, wantViolations: 0},
		{name: "group credit equals ledger debits", sql: `SELECT count(*) FROM rated_usage_groups g WHERE allocated_credit_microcents<>COALESCE((SELECT -sum(amount_microcents) FROM credit_entries e WHERE e.group_id=g.group_id),0)`, wantViolations: 0},
	}
	for _, check := range checks {
		var violations int
		if err := pool.QueryRow(context.Background(), check.sql).Scan(&violations); err != nil {
			t.Fatalf("Invariant %s query: %v", check.name, err)
		}
		if violations != check.wantViolations {
			t.Fatalf("Invariant %s violations=%d; want %d", check.name, violations, check.wantViolations)
		}
	}
}

// assertReferenceInvoices checks every monetary line and price/unit/month field,
// independent rounded totals, line conservation and immutable complete snapshots
// after each later operation. November late usage remains a distinct group.
func assertReferenceInvoices(t *testing.T, store *postgres.Store, want referenceAccount, snapshots map[string]billing.Invoice) {
	t.Helper()
	for _, month := range []string{"2026-10", "2026-11"} {
		if !want.closed[month] {
			continue
		}
		invoice, err := store.Invoice(context.Background(), "acme", month)
		if err != nil {
			t.Fatal(err)
		}
		var groups []referenceGroup
		for _, group := range want.groups {
			if group.billingMonth == month {
				groups = append(groups, group)
			}
		}
		sort.Slice(groups, func(i, j int) bool { return groups[i].usageMonth < groups[j].usageMonth })
		var lines []billing.InvoiceLine
		var gross, credit, deduction, total int64
		for _, group := range groups {
			description := "Usage"
			if group.usageMonth < month {
				description = "Late usage from " + group.usageMonth
			}
			grossCents := referenceRoundedCents(group.gross)
			netCents := referenceRoundedCents(group.gross - group.credit)
			deduction += grossCents - netCents
			total += netCents
			gross += group.gross
			credit += group.credit
			lines = append(lines, billing.InvoiceLine{Kind: "usage", Description: description, AmountCents: grossCents, PriceVersionID: "cpu-acme-2026-10-01", Metric: "cpu_seconds", UsageMonth: group.usageMonth, Units: fmt.Sprint(group.units), GrossMicrocents: fmt.Sprint(group.gross), CreditMicrocents: fmt.Sprint(group.credit)})
		}
		lines = append(lines, billing.InvoiceLine{Kind: "credit", Description: "Used credit", AmountCents: -deduction})
		if invoice.TotalCents != total || invoice.GrossUsageMicrocents != fmt.Sprint(gross) || invoice.CreditUsedMicrocents != fmt.Sprint(credit) || !reflect.DeepEqual(invoice.Lines, lines) {
			t.Fatalf("Invoice %s=%+v; want total=%d gross=%d credit=%d lines=%+v", month, invoice, total, gross, credit, lines)
		}
		var sum int64
		for _, line := range invoice.Lines {
			sum += line.AmountCents
		}
		if sum != invoice.TotalCents {
			t.Fatalf("Invoice %s line sum=%d; want total=%d", month, sum, invoice.TotalCents)
		}
		if previous, found := snapshots[month]; found {
			before, err := json.Marshal(previous)
			if err != nil {
				t.Fatal(err)
			}
			after, err := json.Marshal(invoice)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatalf("Invoice %s changed: before=%s after=%s", month, before, after)
			}
		} else {
			snapshots[month] = invoice
		}
	}
}
