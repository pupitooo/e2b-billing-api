# Financial rules and shared accounting primitives

## Summary

This document defines the shared financial rules, their transaction boundaries,
and the migration and verification requirements:

- [Fixed calendar-month contract](#fixed-calendar-month-contract): whole UTC calendar periods and one immutable invoice per customer/month.
- [Exact money and credit](#exact-money-and-credit): microcent precision, exact charge calculations, signed credit entries, and allocation order.
- [Historical prices and groups](#historical-prices-and-groups): customer overrides, consumption-time pricing, supported intervals, and stable grouping keys.
- [Price insertion contract](#price-insertion-contract): UTC calendar activation, lock-time date validation, stable retries, and controlled historical repairs.
- [Catalog provisioning and P0 recovery](#catalog-provisioning-and-p0-recovery): required dated prices before metering and explicit recovery from a missing valid catalog.
- [Rounding and invoice presentation](#rounding-and-invoice-presentation): cumulative half-up rounding, balancing credit lines, immutable snapshots, and invoice totals.
- [UTC months, limits, add-ons, and late usage](#utc-months-limits-add-ons-and-late-usage): supported years, gross spend limits, full monthly add-on charges, and routing after closure.
- [Transaction and closing contract](#transaction-and-closing-contract): customer locks, atomic financial effects, retries, the processed-usage cutoff, and invoice issuance.
- [Invoice generation and delivery (proposal)](../brainstorming/invoice-scheduling.md#invoice-generation-and-delivery-proposal): first-day generation, customer-selected days, late input, and scheduling safeguards.
- [Migration and verification](../guides/migrations.md#migration-and-verification): converting cent credit to microcents, incompatible legacy allocations, and Go and SQL coverage.

`internal/accounting` implements pure Go calculations for historical rating,
exact credit, UTC months, limits, add-ons, and invoice presentation. The usage API acknowledges durable inbox receipt; the standalone worker applies
these rules transactionally. Financial commands and immutable invoice issuance
are implemented in `internal/billing` and documented in OpenAPI.

The assignment requires monthly invoices over UTC calendar months, historical
prices, customer overrides, usage-only credit, gross monthly limits, full monthly
add-on charges, immutable invoices, and the example's numerical results. The
system must allocate **credit in exact microcents before rounding** and preserve whole
UTC calendar months as fixed billing periods. Half-up rounding, allocation order,
and the processed-usage closing cutoff are implementation policies for this reference
implementation. The [system contract register](system-contracts.md)
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

## Completed-month requirement

The API accepts only `month` as `YYYY-MM`.
It has no closing-day or cutoff parameter; `closed_at` and `issued_at` come from
the server clock. Before starting a new closure, `CloseMonth` requires the server's
UTC time to be at or after the first instant of the following month. January is
eligible from 1 February at `00:00:00Z`; closing it on 20 January returns HTTP `422`
with `invalid_command` and field `month`. Ongoing and future months fail before
publication, without changing credit, usage ratings, invoices,
or numbering. A retry of an already issued invoice returns its original snapshot.
Schedulers must also select completed months. Integration tests supply an explicit
server clock to check the exact UTC boundary and run fixed assignment fixtures;
HTTP callers cannot override that clock. API and worker can select a fixed
business instant at startup with `E2B_DEMO_NOW`; this uses the same completed-month
guard, receipt routing, pricing and audit rules. The unset configuration samples
real time on each operation. Operational deadlines and heartbeats remain real.
See [clock configuration](../guides/runtime-configuration.md#shared-business-clock).

## Exact money and credit

All amounts are USD. Usage charges and credit are stored as **whole microcents**:
one microcent is one millionth of a cent. Code, database domains, and API fields
call this unit `microcents`; `*_microcents` values are amounts in microcents.

```text
1 cent = 1_000_000 microcents
1 USD = 100_000_000 microcents
amount_microcents = amount_cents * 1_000_000
amount_usd = amount_microcents / 100_000_000

charge_microcents = units * price_per_million_cents
used_credit_microcents = min(charge_microcents, available_credit_microcents)
remaining_credit_microcents = available_credit_microcents - used_credit_microcents
net_charge_microcents = charge_microcents - used_credit_microcents
```

`price_versions.price_per_million_cents` stores a **rate in whole cents per
1_000_000 resource units**, not a usage charge. A rate of 5 therefore means
5 microcents per resource unit. Keeping that rate in cents loses no precision;
the calculated charge is in microcents. The general conversion and the separate
price-denominator proposal are explained in [rating](usage-to-invoice.md#4-rating-the-cost-under-the-historical-price).

Go amounts are immutable arbitrary-precision integers; PostgreSQL uses finite
integer `numeric` values. Negative amounts, fractional microcents, and overflow when
converting to `bigint` cents are errors. The signed ledger stores positive grants
and negative usage debits; zero entries are invalid.

A 5_000-microcent charge with one cent available uses 5_000 microcents and retains
995_000 microcents.
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
Sandbox identities and HTTP batch boundaries do not create rounding boundaries. Preserve gross microcents
separately from credit. `booked_charge_cents` remains a rounded gross projection,
not a credit debit or a net invoice amount.

## Accounting resource identities

New `rated_usage_groups.group_id` values use `grp_<positive decimal number>`;
new `credit_entries.credit_entry_id` values use `crd_<positive decimal number>`.
The two independent PostgreSQL `bigint` sequences start at one, persist across
restarts, and allocate distinct numbers across concurrent API and worker instances.
They do not cycle. Exhaustion fails the financial transaction without changing
balances, projections, rating links, or inbox completion. The existing primary
keys remain the database integrity constraint; allocation needs no extra lookup
for ID collisions or random regeneration.

Sequence allocation is not rolled back, so failed transactions can leave gaps.
IDs are identifiers, not row counts or a guaranteed ordering of committed actions.
Uniqueness applies within each database's sequence namespace; independent database
copies do not share allocation. Do not reset or cycle an active sequence.

The worker finds an existing group by
`(customer_id, price_version_id, usage_month, billing_month)` under the customer
lock. It reuses that group's stored ID, including earlier hash IDs, and allocates
a number only for a new group. Credit grants check the scoped API
`idempotency_key` in command history before allocating a ledger entry. Identical
retries allocate no new entry. Grants and usage debits both use independent
server-generated `credit_entry_id` values. Usage replay is prevented by the
unique `(source, event_id)` rating link and the locked receipt checkpoint, committed
atomically with ledger and balance writes.

Migration `012_accounting_id_sequences.sql` adds the owned sequences without
rewriting any IDs, financial amounts, references, or frozen invoices. If earlier
rows already contain canonical `grp_` or `crd_` decimal IDs within the supported
sequence range, the migration starts allocation after the highest reserved number.
Apply the migration before starting the updated API and worker.

## Price insertion contract

Billing generates price resource IDs and derives new subscription IDs from the
structured `(customer_id, addon_name)` identity using
`subscription/<customer_id>/<addon_name>`. Each component is independently
percent-encoded as a URL path segment, preserving separators, case, percent signs,
and UTF-8 bytes without ambiguity. The two existing 256-byte input limits yield a
maximum of 1550 ASCII bytes for the first-purchase ID; reactivation appends `/YYYY-MM-DD` and permits up to 1561 bytes. Existing IDs and persisted
results retain their original values. The caller supplies an independent
JSON `idempotency_key` for prices, add-on purchases and cancellations, credit grants, and spend-limit changes. The
append-only `api_idempotency_operations` table records the scope, key, normalized
original request, successful result, and creation time. The price scope is global
for `/prices`; the add-on purchase/cancellation, credit, and spend-limit scopes include the customer. Keys are case-sensitive
visible ASCII tokens of at most 256 bytes and are retained indefinitely.

A transaction-scoped advisory lock serializes the scope/key before reading
history; hash collisions only serialize unrelated commands. The composite primary
key determines identity. Financial writes and result recording commit together.
A failed command reserves no key; unchanged retries return the stored result after
process restarts, activation, catalog changes, or closing. Changed values conflict.
All financial command types, including cancellation, use this one history table. Credit grants additionally
snapshot their internal ledger ID in the stored result; caller keys never become
accounting or resource IDs. A retry of an older limit command preserves any newer
configuration.

The revised migration foundation requires a clean schema; it does not backfill
keys or upgrade the previous split command histories.

`POST /prices` requires `effective_from` as a valid Gregorian date in
`YYYY-MM-DD` format, years 1000 through 9999. Timestamps, offsets, null, and
missing dates are rejected. The application resolves the date to midnight UTC,
supplies that instant explicitly to PostgreSQL, and returns a date in the public
response. Activation always applies to the whole UTC calendar day.

The requested date must be today or later according to the server clock sampled
after acquiring the exclusive catalog lock and checking the scoped request key.
Yesterday returns HTTP `422`, `invalid_command`, field `effective_from`, without
catalog or financial writes. Today's date remains valid when submission or lock
waiting advances the clock within the same UTC day. Crossing midnight during a
lock wait can make the requested date past. Validation never uses local time.

Already rated usage is protected even when a new version starts today. If the
new version would change a selected historical price or split an already rated
interval, the transaction returns `409` without financial effects. Unprocessed
usage uses the version valid on its consumption day. This policy permits a
whole-day start; it never recalculates existing allocations or invoices.

An unchanged existing request key returns its original price after activation.
Changed customer, metric, amount, or date returns `409`. A retry never resolves a
new activation date. Stored historical versions remain finite, non-null instants;
controlled operator repairs retain their existing responsibilities.

Usage remains priced at consumption time. Two increments
from 1 February both use price 100, even when one arrives on 10 February after
price 200 was inserted and became effective on 2 February. Only consumption
from the activation instant uses 200. Invoice closure affects billing-month
routing, not historical price selection.

Initial historical seed data and investigated catalog-gap repair are controlled
database operations, outside ordinary `POST /prices`. Repairs must acquire the
exclusive catalog advisory lock, preserve all rated history and issued invoices,
and explicitly release only the investigated receipt. They do not implement
retroactive repricing; no automatic recalculation exists.

## Credit audit time and add-on purchase dates

Credit commands contain only `idempotency_key` and `amount_cents`. The API neither
accepts nor returns `recorded_at`. After the operation replay check and account
lock, the backend samples its clock, converts to UTC, and truncates to microsecond
precision. This audit instant is written explicitly to both the credit ledger and
command history in the same transaction. A replay retains the original ledger
entry, audit time, balance, and state version; only changed amounts conflict.
An unsupported backend clock fails with HTTP `503` and no financial writes; it
is never reported as a caller validation error for an audit field.

Add-on commands require `purchased_at` as `YYYY-MM-DD`, years 1000 through 9999.
The backend stores midnight UTC and returns the date, deriving `start_month` from
that same UTC day. Historical dates in open months and future dates are allowed;
a new purchase in or before any closing or closed month returns `409`. The full
catalog price is charged in every active month starting with the purchase month.
Identical retries return the original subscription even after catalog changes or
invoice closure; changing the date under the same key conflicts.

The database keeps `timestamptz` columns for internal accounting instants and
historical records. This API change requires no schema migration or rewrite of
append-only history. New application price and purchase inputs require midnight
UTC; credit audit instants remain precise timestamps owned by the backend.

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
round_cents(microcents) = floor((microcents + 500_000) / 1_000_000)
gross_cents = round_cents(group_gross_microcents)
net_cents = round_cents(group_gross_microcents - group_credit_microcents)
credit_line_cents = -(gross_cents - net_cents)
```

`InvoiceAmounts` returns the positive credit deduction; the invoice writer
uses its negative sign. Derive the credit presentation from gross and net, rather
than independently rounding credit, so displayed lines sum to rounded net usage.
For one cent gross and half a cent credit, net rounds to one cent and displayed
credit is zero cents. The ledger still records the exact half-cent debit.

Immutable invoice snapshots retain exact gross and credit microcents for
audit alongside cent presentation. Presentation never overwrites credit records.
Sum already rounded group lines without another rounding step. Add-ons use their
purchased whole-cent price. Invoice totals need checked addition within `bigint`.

A positive usage charge below half a cent after credit rounds to zero cents.
Zero-cent usage lines and zero-total invoices are valid: retain every processed
group's complete resource units, original usage month, price version, and exact
gross and credit microcents. A zero-total invoice still closes its month, freezes its
groups, and reserves its ordinary invoice number; reads and retries preserve it.
There is no minimum charge and no carry of a rounding remainder into another
group or invoice. Exact historical microcents remain available for audit, limits,
and credit accounting; they are not an unpaid balance to collect later.
For example, uncredited usage of USD 0.002 in each of three months produces
three zero-total invoices, even though its combined value is USD 0.006.
Exactly half a cent rounds up to one cent. Credit continues to be consumed
in exact microcents before presentation, including when the displayed amount is zero.
The assignment requires cent-valued invoices but specifies neither a minimum
charge nor remainder carry; this is the system's explicit rounding policy.

Migration 007 introduced invoice snapshots, group freezes and per-customer numbering alongside the former closing cohorts. Migration 009 retires cohort bookkeeping; invoices are now published directly from processed groups.

## UTC months, limits, add-ons, and late usage

Consumption, receipt times, and accounting months support UTC years 1000 through
9999. Bounds apply after converting offsets to UTC; a local year alone does not
determine validity. Calendar years retain ordinary digits without underscores.

`UsageMonth` uses the original UTC month regardless of timestamp offset.
`LimitReached` compares that month's exact gross microcents with the limit converted
from cents. Credit and add-ons are excluded. `NULL` means unlimited; zero is reached
even with no spend. All measured usage remains billable after reaching a limit.
Raising the limit or selecting the new UTC month's gross total can clear it.

Limit status reflects committed accounting and can lag measured or accepted usage.
The [delayed-status limitation](limitations.md#l1-delayed-spend-limit-status)
records the rationale, unmeasured delay and financial impact, and investigation plan.

`AddonCharge` bills the full purchased price from the UTC purchase month onward,
including purchases at month end. Earlier months cost zero. Storage selects only
active purchase periods. `POST /customers/{customer_id}/addons/cancel` appends an
immutable termination record with a caller-supplied `cancelled_at` UTC date.
That month is charged in full; following months omit the purchase. Cancellation
cannot precede purchase or target a month at or before the latest closure.
Historical open dates and future dates are allowed, matching the purchase policy.
Identical retries retain the original termination after closing and reactivation;
a new key cannot replace an existing termination. A later purchase must start
strictly after every earlier purchase's inclusive end month and snapshots the
current catalog price. It receives the readable base ID plus `/YYYY-MM-DD`;
first purchase IDs and all historical command results remain intact. This suffix
raises the maximum generated ID length to 1561 bytes.

Full fees for active months follow the assignment. Inclusive cancellation months,
no overlapping activation months, append-only termination and date-suffixed
reactivation IDs are implementation policies. Quantities and proration remain
proposals. See the [delayed billing scenario](../simulator/delayed-billing.md)
for the combined credit, limit, cancellation and backlog example.

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

The [deferred-invoicing limitation](limitations.md#l2-accepted-usage-on-a-later-invoice)
records how to measure the additional deferral beyond late platform delivery.
The [rounding limitation](limitations.md#l3-rounding-across-groups-and-invoices)
records the possible cent impact of separate invoice boundaries.

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
earlier usage in `usage_inbox`, earlier unclosed rated groups, or an active
subscription whose `start_month` precedes the target month. A purchase alone
establishes the first required add-on month, even without usage or available credit.
Existence checks read the older-month boundaries; they neither select a processing
cohort nor rates usage. After any committed closure, each next new invoice requires
the immediately preceding month closed, including empty months. Missing
predecessors and skipped older data return HTTP `409` without publication. The
check uses the UTC original usage month, not `received_at`. Inbox rows committed
after the check do not change its result; subsequent worker accounting still
respects the committed closed history. No separate first-month setting is needed.

For example, buying `concurrency_pack` on `2026-10-05` and first requesting
November returns `409`, leaving invoices, numbering, subscriptions and credit
unchanged. Close October first, then November; each contains the full 2_000-cent
fee. Skipping several active months is rejected the same way. A subscription
starting in the target month is included immediately; one starting later does
not block that invoice. Purchase and closing use the same account lock: an older
purchase that commits first blocks a later first invoice; closing that commits
first rejects a purchase in or before the closed month.

On pre-commit failure, the complete closing transaction rolls back; a retry can
include additional groups committed since the failed attempt. After a successful
commit, every retry returns the original invoice and number. Earlier unclosed
rated groups still block later closing; an empty month is supported.

Usage processed on opposite sides of this cutoff belongs to different billing
months and therefore different groups and rounding boundaries. Exact gross microcents
are preserved, but the sum of cent-valued invoices can differ from rounding all
usage in one group: two half-cent charges in separate invoices round to one cent
each, while together they round to one cent. Credit follows worker transaction
order and the balance available when each usage is processed.

Used prices and issued invoices cannot be silently rewritten. Retiring the old
cohort and exclusion tables does not change the explicit operator recovery of
quarantined usage or the worker's atomic financial transaction.

## Money and months

Usage charges, credit balances, allocations, and ledger entries are in microcents;
see [the unit definition and conversions](#exact-money-and-credit). Catalog rates,
limits, add-on prices, and invoice presentation retain their whole-cent contracts.
PostgreSQL integer-valued `numeric` and Go `big.Int` preserve the exact sums;
conversion to signed 64-bit cents checks overflow.

Cyberdyne's first 123_456_789 units at 5 cents per million give:

```text
charge_microcents = 123_456_789 × 5 = 617_283_945
charge_usd = 617_283_945 / 100_000_000 = USD 6.172_839_45
booked_gross_cents = floor((617_283_945 + 500_000) / 1_000_000) = 617
```

The group retains 617_283_945 microcents in `exact_charge_microcents` and 617 cents in
`booked_charge_cents`. New usage adds to the microcent total before the gross
projection is rounded again. Credit uses the exact new charge before rounding;
`allocated_credit_microcents` cannot exceed `exact_charge_microcents`. Migrations define
storage and constraints; the application performs these calculations.

`usage_month` and `billing_month` are finite first-of-month `date` values. Application code derives them in UTC. Late October usage billed in November retains `usage_month = 2026-10-01` and `billing_month = 2026-11-01`; its gross spend belongs to October. Billing months cannot precede usage months. Timestamps are finite `timestamptz` values. Subscription start months are checked against the purchase timestamp in UTC, independently of the SQL session's time zone.

**The fixed billing contract is one immutable invoice per customer for a whole
UTC calendar month.** A chosen generation or delivery day cannot change the
period into a cycle starting on that day. `(customer_id, billing_month)` identifies
both the final closure and invoice; `closed_at` records the actual closure time,
not the end of a custom usage interval. See the [fixed calendar-month contract](#fixed-calendar-month-contract)
and the [completed-month requirement](#completed-month-requirement).
Scheduling and delivery remain [open proposals](../brainstorming/invoice-scheduling.md).

Default and customer prices can coexist at the same instant. `UNIQUE NULLS NOT DISTINCT (customer_id, metric, effective_from)` prevents two default prices at that instant as well as duplicate customer versions; see [PostgreSQL unique constraints](https://www.postgresql.org/docs/18/ddl-constraints.html#DDL-CONSTRAINTS-UNIQUE-CONSTRAINTS). The rater selects the latest eligible customer override first and otherwise the latest eligible default. It rejects unsupported segments crossing a price boundary; the database does not select the applicable price.

Every price has a required finite `effective_from`; no undated baseline is supported.
The seed preserves the assignment's original effective starts. Provision an additional
metric and its applicable price before the platform generates its first usage, using
a start covering that consumption. Missing a valid price is a P0 catalog incident:
the receipt retains `P0 missing_valid_price: ...` without financial effects and the
receipt orchestrator logs the incident after commit during worker processing.
Invoice closing never processes input or emits this incident. Repair the historical
catalog through [controlled operator SQL](../guides/accounting-recovery.md#controlled-historical-price-repair),
then explicitly release the investigated receipt for another attempt. Ordinary
`POST /prices` accepts `effective_from` as a required `YYYY-MM-DD` UTC date and rejects
dates before today, with rated-history protection for today. Public purchase dates
use the same date format and are stored at midnight UTC. Credit audit timestamps
are generated by the backend and are absent from grant requests and responses. See the [provisioning and recovery contract](#catalog-provisioning-and-p0-recovery).

For operational upgrade procedures, read [migrations](../guides/migrations.md).
Scheduling and delivery remain [open proposals](../brainstorming/invoice-scheduling.md).
