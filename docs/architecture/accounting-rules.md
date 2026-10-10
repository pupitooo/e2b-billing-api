# Financial rules and shared accounting primitives

## Summary

This document defines the shared financial rules, their transaction boundaries,
and the migration and verification requirements:

- [Fixed calendar-month contract](#fixed-calendar-month-contract): whole UTC calendar periods and one immutable invoice per customer/month.
- [Exact money and credit](#exact-money-and-credit): tick precision, exact charge calculations, signed credit entries, and allocation order.
- [Historical prices and groups](#historical-prices-and-groups): customer overrides, consumption-time pricing, supported intervals, and stable grouping keys.
- [Price insertion contract](#price-insertion-contract): no backdated ordinary changes, lock-time validation, stable retries, and controlled historical repairs.
- [Catalog provisioning and P0 recovery](#catalog-provisioning-and-p0-recovery): required dated prices before metering and explicit recovery from a missing valid catalog.
- [Rounding and invoice presentation](#rounding-and-invoice-presentation): cumulative half-up rounding, balancing credit lines, immutable snapshots, and invoice totals.
- [UTC months, limits, add-ons, and late usage](#utc-months-limits-add-ons-and-late-usage): supported years, gross spend limits, full monthly add-on charges, and routing after closure.
- [Transaction and closing contract](#transaction-and-closing-contract): customer locks, atomic financial effects, retries, the processed-usage cutoff, and invoice issuance.
- [Invoice generation and delivery (proposal)](#invoice-generation-and-delivery-proposal): first-day generation, customer-selected days, late input, and scheduling safeguards.
- [Migration and verification](#migration-and-verification): converting cent credit to ticks, incompatible legacy allocations, and Go and SQL coverage.

`internal/accounting` implements pure Go calculations for historical rating,
exact credit, UTC months, limits, add-ons, and invoice presentation. The usage API acknowledges durable inbox receipt; the standalone worker applies
these rules transactionally. Financial commands and immutable invoice issuance
are implemented in `internal/billing` and documented in OpenAPI.

The assignment requires monthly invoices over UTC calendar months, historical
prices, customer overrides, usage-only credit, gross monthly limits, full monthly
add-on charges, immutable invoices, and the example's numerical results. The
system must allocate **credit in exact ticks before rounding** and preserve whole
UTC calendar months as fixed billing periods. Half-up rounding, allocation order,
and the processed-usage closing cutoff are implementation policies for this reference
implementation. The [system contract register](../../README.md#system-contracts)
distinguishes assignment requirements from these additional policies.

## Fixed calendar-month contract

**Billing periods are whole UTC calendar months. This is a fixed domain contract,
not a customer preference or a scheduling option.** January 2027 means the
half-open interval `[2027-01-01T00:00:00Z, 2027-02-01T00:00:00Z)`.

- `billing_month` identifies the first UTC date of that calendar month. It is not
  a chosen closing day or the date on which the customer receives a document.
- There is one final invoice per customer/billing month. Final closure and invoice
  publication share that identity; retries return the original snapshot and number.
- A preferred generation or delivery day never changes the period's boundaries.
  Generating January's invoice on 20 February still bills the January calendar
  period. It does not create a cycle from 20 January to 20 February.
- Spend limits and monthly add-ons keep their UTC calendar-month rules regardless
  of when the invoice is generated or delivered.
- Issued invoices and closed months remain immutable. Usage not yet processed at the closing cutoff follows the existing late-usage
  policy, even if it was accepted earlier. No later processing reopens an invoice.

Whole months define accounting periods; they do not guarantee that all delayed
measurements have arrived by issuance. A later invoice can therefore contain
usage from an earlier month. Preserve its original `usage_month` for historical
prices, gross spend limits, and audit while recording the later `billing_month`.

## Exact money and credit

All amounts are USD. One cent is 1_000_000 ticks; one tick is USD 0.000_000_01.
Prices remain whole cents per million resource units. The price denominator and
money resolution are separate constants:

```text
price_unit_count = 1_000_000
ticks_per_cent = 1_000_000
gross_charge_ticks = (((units * price_per_million_cents) * ticks_per_cent) / price_unit_count)
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
Later default changes do not mask an eligible override. A missing valid price is
a P0 catalog incident; ambiguous relevant prices also fail visibly.

One receipt is a half-open interval `[period_start, period_end)` wholly within
one UTC month and one applicable price version. Ending exactly at a boundary is
valid. Unsupported crossing intervals are errors; uniform spreading of units is
not assumed. Supporting multiple segments would require extending the current
one-receipt-to-one-group relationship.

The stable group key is `(customer_id, price_version_id, usage_month, billing_month)`.
Sandbox and batch IDs do not create rounding boundaries. Preserve gross ticks
separately from credit. `booked_charge_cents` remains a rounded gross projection,
not a credit debit or a net invoice amount.

## Price insertion contract

New ordinary price versions must have `effective_from` at or after the server's
insertion instant, sampled after the exclusive catalog lock is acquired and the
identity is checked. Equality is accepted; future activation can be scheduled.
A past instant returns HTTP `422`, `invalid_command`, field `effective_from`,
without a catalog or financial write. An unchanged existing identity succeeds
on retry even after activation; changed content still returns `409`.

Keep the catalog lock and rated-history protection. Waiting for the lock can
make a previously future instant invalid, so request arrival time is not the
validation boundary. Usage remains priced at consumption time. Two increments
from 1 February both use price 100, even when one arrives on 10 February after
price 200 was inserted and became effective on 2 February. Only consumption
from the activation instant uses 200. Invoice closure affects billing-month
routing, not historical price selection.

Initial historical seed data and investigated catalog-gap repair are controlled
database operations, outside ordinary `POST /prices`. Repairs must acquire the
exclusive catalog advisory lock, preserve all rated history and issued invoices,
and explicitly release only the investigated receipt. They do not implement
retroactive repricing; no automatic recalculation exists.

## Catalog provisioning and P0 recovery

Every price must retain an explicit finite, non-null `effective_from`.
There is no undated baseline and no implied price before
the first eligible version. The assignment seed supplies its exact dated default
prices and Acme override. Preserve those amounts and start times.

Provision each additional metric and its applicable price before the platform
generates the first measurement. The price's `effective_from` must be on or before
that measurement's `period_start`. A default price covers customers without an
eligible override; a price owned by another customer cannot substitute for it.
This is a provisioning contract, not a database guarantee that every metric has
a price for every historical instant. An explicitly configured zero price is valid.

If rating cannot find an eligible price, `accounting.MissingPriceError` identifies
the customer, metric, and original consumption time. Accounting stores
`P0 missing_valid_price: ...` in `usage_inbox.processing_error`, leaves
`processed_at` NULL, and creates no group, rating link, credit debit, gross-spend
projection, or account-state change for that receipt. After the quarantine commits,
the worker receipt orchestration emits an ERROR report with `priority=P0`,
`error_code=missing_valid_price`, source/event identity, customer, metric, and
`period_start`. The standalone worker uses its JSON logger for this report.
Closing never invokes receipt processing or discovers new catalog incidents.
Automatic polling skips quarantined usage and continues with other pending input.

Treat the missing catalog as an immediate P0 repair: append the correct price
through controlled operator SQL, with an explicit start covering the original measurement.
The historical protection still rejects a version that invalidates already rated
usage. After the repair, an operator explicitly clears the investigated receipt's
error using the [recovery procedure](../guides/accounting-recovery.md#investigate-and-release-a-receipt).
Adding a price or repeating the usage request does not automatically release it.
The next attempt accounts for the original receipt exactly once and follows the
existing open-month routing. Every non-null processing error excludes the receipt
from invoice issuance, regardless of its cause. It cannot block closing or change
an already issued invoice.

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
Migration 007 introduced invoice snapshots, group freezes and per-customer numbering alongside the former closing cohorts. Migration 009 retires cohort bookkeeping; invoices are now published directly from processed groups.

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

`BillingMonth` preserves the original month only while chronological closing
allows its invoice: no same or later month has been closed. Otherwise choose a
month strictly after the latest closed month and at or after the receipt month.
This also handles a skipped earlier empty month whose pending usage is processed
later. October usage received in November after October closing bills in November,
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

**Closing invoices only usage whose accounting transaction committed before the
customer account lock was acquired.** It reads `rated_usage_groups` for the target
billing month, never processes pending `usage_inbox`, and does not wait
for the worker to drain a backlog. Pending and quarantined usage in the target
month cannot block a valid chronological issuance. This explicitly accepts an incomplete usage invoice in exchange for a
simpler closing operation independent of the pending usage count.

Closing owns one transaction and holds the same account lock used by the worker
until commit or rollback. An in-flight worker that already holds the lock must
finish before closing can acquire it; its committed groups are included. A worker
that acquires the lock after a successful closing sees the committed closed month.
It preserves an original month after the latest closure; usage from that closure
or an earlier month routes strictly after it and at or after the receipt month.
Skipped earlier empty
months cannot strand pending usage behind a later closure. Preserve historical consumption-time prices and
original-month gross spend. Receipt timestamps, delivery order and durable inbox
acceptance do not promise inclusion in the original month's invoice.

Validate the ended UTC month and chronological history, build lines from processed
groups and applicable add-ons, freeze groups, record the closed month and immutable
invoice snapshot, and advance numbering and state version together. Do not debit
credit again. There is no durable intermediate closing marker or usage list.
The first closing may select any completed month that does not skip existing
earlier usage in `usage_inbox` or earlier unclosed rated groups. An indexed
existence check reads the older-month boundary; it neither selects a processing
cohort nor rates usage. After any committed closure, each next new invoice requires
the immediately preceding month closed, including empty months. Missing
predecessors and skipped older data return HTTP `409` without publication. The
check uses the UTC original usage month, not `received_at`. Inbox rows committed
after the check do not change its result; subsequent worker accounting still
respects the committed closed history. No separate first-month setting is needed.

On pre-commit failure, the complete closing transaction rolls back; a retry can
include additional groups committed since the failed attempt. After a successful
commit, every retry returns the original invoice and number. Earlier unclosed
rated groups still block later closing; an empty month is supported.

Usage processed on opposite sides of this cutoff belongs to different billing
months and therefore different groups and rounding boundaries. Exact gross ticks
are preserved, but the sum of cent-valued invoices can differ from rounding all
usage in one group: two half-cent charges in separate invoices round to one cent
each, while together they round to one cent. Credit follows worker transaction
order and the balance available when each usage is processed.

Used prices and issued invoices cannot be silently rewritten. Retiring the old
cohort and exclusion tables does not change the explicit operator recovery of
quarantined usage or the worker's atomic financial transaction.

## Invoice generation and delivery (proposal)

The following scheduling choices are proposals, not implemented customer settings
or a selected product policy. Both preserve the fixed calendar-month contract.
Final closure and invoice publication happen in the same transaction; delivering
the resulting document is a separate operation. An unfinished preview, if added
later, must remain distinguishable from an issued immutable invoice.

Assume earlier customer months are closed, February is open, and accepted input
can be processed successfully. For the January 2027 invoice:

| Policy | Final closure and invoice generation | Delivery | January usage received on 5 February |
| --- | --- | --- | --- |
| Generate on the first day; deliver on the selected day | 1 February | 20 February | Appears on a later invoice because January is already closed. |
| Generate and deliver on the selected day | 20 February | 20 February | Remains eligible for January if accounting commits before closing acquires the account lock. |

The selected generation day provides a longer window for late input, at the cost
of keeping the preceding month open and its final amount unavailable for longer.
A selected delivery day changes notification timing; it never recalculates an
already issued invoice. Neither choice guarantees that no input arrives later.
Usage accounting and credit allocation continue independently of this schedule.

**Recommended first extension:** run a monthly job on the first UTC day and call
the existing invoice API for the preceding month of each customer. Keep generation
and delivery separate, and add customer-specific generation days only if waiting
for late input or a preferred issue date is a product requirement. For a customer
with generation day 20, run January's close on 20 February and February's close
on 20 March. A missed run must recover earlier missing months in increasing order.
One customer's failed close must not prevent other customers from progressing;
retry the same customer/month without reserving another invoice number.

Before implementing customer schedules, choose the execution time in UTC, the
rule for days 29–31 in shorter months, the effective date of preference changes,
and recovery after outages. Generation and delivery need separate durable work
tracking and retry identities. Delivery retries must not generate another invoice;
provider idempotence, when available, is separate from database invoice uniqueness.

**Completed-month requirement:** the API accepts only `month` as `YYYY-MM`.
It has no closing-day or cutoff parameter; `closed_at` and `issued_at` come from
the server clock. Before starting a new closure, `CloseMonth` requires the server's
UTC time to be at or after the first instant of the following month. January is
eligible from 1 February at `00:00:00Z`; closing it on 20 January returns HTTP `422`
with `invalid_command` and field `month`. Ongoing and future months fail before
publication, without changing credit, usage ratings, invoices,
or numbering. A retry of an already issued invoice returns its original snapshot.
Schedulers must also select completed months. Integration tests supply an explicit
server clock to check the exact UTC boundary and run fixed assignment fixtures;
HTTP callers cannot override that clock.

The scheduler, customer day settings, document delivery, and an optional preview
are not implemented. Whole UTC calendar months remain a fixed system contract;
the scheduling alternatives remain open proposals.

## Migration and verification

For migration `009_processed_usage_closing.sql`, stop the API and worker before
running `make migrate`, then restart them with the updated code. It removes the
former closing-attempt, receipt-cohort and exclusion tables, including unfinished
attempts. Raw usage, financial history, group freezes and issued invoices remain.
Tests upgrade existing pending and quarantined attempts in UTC and Asia/Shanghai
and verify retained balances, numbers and invoice immutability.

Earlier migration `004_exact_credit.sql` converts:

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
