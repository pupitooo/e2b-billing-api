# Financial rules and shared accounting primitives

`internal/accounting` implements pure Go calculations for historical rating,
exact credit, UTC months, limits, add-ons, and invoice presentation. The usage API acknowledges durable inbox receipt; the standalone worker applies
these rules transactionally. Financial commands and immutable invoice issuance
are implemented in `internal/billing` and documented in OpenAPI.

The assignment requires historical prices, customer overrides, usage-only credit,
gross monthly limits, full monthly add-on charges, immutable invoices, and the
example's numerical results. On 9 October 2026 the user selected **credit in ticks
before rounding**. Half-up rounding, allocation order, and the closing contract
below are implementation assumptions for this reference implementation.

## Exact money and credit

All amounts are USD. One cent is 1_000_000 ticks; one tick is USD 0.000_000_01.
Prices remain whole cents per million resource units. The price denominator and
money resolution are separate constants:

```text
price_unit_count = 1_000_000
ticks_per_cent = 1_000_000
gross_charge_ticks = units * price_per_million_cents * ticks_per_cent / price_unit_count
used_credit_ticks = min(new_charge_ticks, available_credit_ticks)
remaining_credit_ticks = available_credit_ticks - used_credit_ticks
net_charge_ticks = new_charge_ticks - used_credit_ticks
```

`accounting.PriceUnitCount` and `accounting.TicksPerCent` currently cancel in the
charge formula. Multiply before dividing with arbitrary-precision integers and
reject a nonzero remainder. [Issue #29](https://github.com/pupitooo/e2b-billing-api/issues/29)
proposes a denominator stored on each historical price version, preserving the
current money resolution and backfilling legacy versions with 1_000_000.

Go amounts are immutable arbitrary-precision integers; PostgreSQL uses finite
integer `numeric` values. Negative amounts, fractional ticks, and overflow when
converting to `bigint` cents are errors. The signed ledger stores positive grants
and negative usage debits; zero entries are invalid.

A 5_000-tick charge with one cent available uses 5_000 ticks and retains 995_000.
Never round a debit or balance to cents. Credit pays usage only, never add-ons.
Allocate credit in the order transactions serialize on the customer's account
lock, which need not be historical measurement order across delayed events.
Later grants pay newly processed consumption without rewriting earlier allocations.
Splitting one open group preserves its gross, credit, and net totals with the same
available credit and no intervening grants; this does not promise order independence
across different groups or new grants.

## Historical prices and groups

`Rate` supports schema version 1. Choose the latest eligible customer override,
otherwise the latest eligible default. Use consumption time, never receipt time.
Later default changes do not mask an eligible override. Missing or ambiguous
relevant prices fail visibly.

One receipt is a half-open interval `[period_start, period_end)` wholly within
one UTC month and one applicable price version. Ending exactly at a boundary is
valid. Unsupported crossing intervals are errors; uniform spreading of units is
not assumed. Supporting multiple segments would require extending the current
one-receipt-to-one-group relationship.

The stable group key is `(customer_id, price_version_id, usage_month, billing_month)`.
Sandbox and batch IDs do not create rounding boundaries. Preserve gross ticks
separately from credit. `booked_charge_cents` remains a rounded gross projection,
not a credit debit or a net invoice amount.

## Rounding and invoice presentation

Use half-up on each cumulative frozen group, never on individual events:

```text
round_cents(ticks) = floor((ticks + 500_000) / 1_000_000)
gross_cents = round_cents(group_gross_ticks)
net_cents = round_cents(group_gross_ticks - group_credit_ticks)
credit_line_cents = -(gross_cents - net_cents)
```

`InvoiceAmounts` returns the positive credit deduction; the invoice writer
uses its negative sign. Derive the credit presentation from gross and net, rather
than independently rounding credit, so displayed lines sum to rounded net usage.
For one cent gross and half a cent credit, net rounds to one cent and displayed
credit is zero cents. The ledger still records the exact half-cent debit.

Immutable invoice snapshots retain exact gross and credit ticks for
audit alongside cent presentation. Presentation never overwrites credit records.
Sum already rounded group lines without another rounding step. Add-ons use their
purchased whole-cent price. Invoice totals need checked addition within `bigint`.
Migration 007 adds durable closing cohorts, immutable invoice snapshots, frozen-group links, and per-customer invoice sequences.

## UTC months, limits, add-ons, and late usage

Consumption, receipt times, and accounting months support UTC years 1000 through
9999. Bounds apply after converting offsets to UTC; a local year alone does not
determine validity. Calendar years retain ordinary digits without underscores.

`UsageMonth` uses the original UTC month regardless of timestamp offset.
`LimitReached` compares that month's exact gross ticks with the limit converted
from cents. Credit and add-ons are excluded. `NULL` means unlimited; zero is reached
even with no spend. All measured usage remains billable after reaching a limit.
Raising the limit or selecting the new UTC month's gross total can clear it.

`AddonCharge` bills the full purchased price from the UTC purchase month onward,
including purchases at month end. Earlier months cost zero. Cancellation,
quantities, and proration remain outside the assignment's contract.

`BillingMonth` preserves an open original month. For a closed original month,
choose the first open month at or after both the original month and first receipt
month. October usage received in November after October closing bills in November,
keeps October's historical price, and contributes to October gross spend. If
November is also closed, move forward again. The helper takes closure state as
input; it does not create or persist that state.

## Transaction and closing contract

Every financial writer first locks `customer_billing_state` with `FOR UPDATE`,
before reading mutable balances or closure state. The worker processes one receipt for one customer per transaction. Never hold inbox row locks while waiting
for the account lock; consistent ordering avoids deadlocks with closing and grants.

Commit the rating link, group totals, credit allocation/debit/balance, original-month
gross projection, state version, and inbox completion in one transaction. Stable
receipt and operation identities protect retries. Cancellation or failure rolls
back the complete financial effect. Transient failures retry; unsupported inputs
remain available with a visible processing error and explicit recovery path.

Closing establishes a fixed cohort of already committed accepted receipts, then
drains it without holding the worker's account lock. Processing errors in that
cohort block closing. Under the account lock, recheck the cohort, freeze groups,
record the closed month, create immutable invoice snapshots, and allocate the
customer's next number in one transaction. Do not consume credit again. Later
accepted input follows the late-usage rule, including a request whose receipt
timestamp predates its eventual commit.

The invoice implementation persists cohort membership and closing state; a retry
resumes after fixing the supported catalog and explicitly releasing processing errors. A receipt timestamp alone is not a closing
watermark. Used historical prices and issued invoices cannot be silently rewritten
by a later command; retroactive corrections require a separate policy.

## Migration and verification

Run the existing `make migrate`. Migration `004_exact_credit.sql` converts:

| Previous column | Current column | Conversion |
| --- | --- | --- |
| `customer_billing_state.credit_balance_cents` | `credit_balance_ticks` | cents × 1_000_000 |
| `rated_usage_groups.allocated_credit_cents` | `allocated_credit_ticks` | cents × 1_000_000 |
| `credit_entries.amount_cents` | `amount_ticks` | signed cents × 1_000_000 |

Identities, signs, timestamps, operation keys, row counts, limits, and state
versions survive. The runner commits schema changes and the version together
under its advisory lock; repeated runs skip the conversion.

Legacy allocations can exceed exact gross because the old model allowed credit
up to rounded gross cents. Such groups need a reviewed accounting correction.
The migration stops before changing history and rolls back atomically. On the
old schema, inspect affected groups with:

```sql
SELECT group_id, exact_charge_ticks, allocated_credit_cents
FROM rated_usage_groups
WHERE allocated_credit_cents::numeric * 1_000_000 > exact_charge_ticks;
```

Go tests cover arbitrary precision, half-cent boundaries, exact credit, split
events, historical prices, UTC and price boundaries, routing, limits, add-ons,
and assignment totals of USD 20.00, USD 20.00, and USD 18.17 with credit balances
of USD 13.00 and USD 7.00. SQL tests exercise real legacy conversion, sub-cent
credit, constraints, and append-only history in UTC and Asia/Shanghai. These
verify calculations and schema; runtime accounting, public command, and immutable invoice tests now cover their implementations.
