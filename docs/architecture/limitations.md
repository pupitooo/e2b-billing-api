# Limitations and investigation

The asynchronous accounting and invoice cutoff are deliberate design choices.
Their operating impact needs measurement: the prototype does not establish a
maximum status delay, a small deferred-usage share, or an acceptable aggregate
rounding difference. We explicitly account for these limitations and will decide
whether to retain the policies or introduce mitigation from measured results.

This page records the behavior, rationale, and investigation plan. It complements
the [implemented contracts](implemented-contracts.md) and [financial rules](accounting-rules.md).
The operating targets below need agreement for a real deployment; they do not
add requirements to the assignment. Proposed instrumentation and mitigation are
not implemented guarantees.

| ID | Known behavior | Impact still to investigate |
| --- | --- | --- |
| L1 | Limit status includes committed accounting, not all measured or accepted usage. | End-to-end delay, additional spend before platform reaction, and recovery time after an outage. |
| L2 | Closing includes processed groups; previously accepted pending or quarantined usage can appear on a later invoice. | Additional deferred usage beyond late platform delivery, its value, and customer impact. |
| L3 | Each group has its own cent-rounding boundary; no remainder carries between invoices. | Aggregate differences caused by grouping and deferral. |

## L1: delayed spend-limit status

### Behavior and rationale

`POST /usage/batches` returns `202` after durable storage in usage_inbox. The independent
worker subsequently commits rating, original-month gross spend, credit allocation,
and usage_inbox row completion together. The [limit-status endpoints](submission-overview.md#interfaces-and-ownership)
read this committed gross spend; they exclude credit and add-ons.

For example, Cyberdyne has exact October gross spend of USD 6.17283945 and a
USD 15 limit. An additional USD 12 can already be accepted while status still
reports `limit_reached=false`. After accounting commits, gross spend is
USD 18.17283945 and status reports `true`. Periodic platform polling adds another
delay before the platform observes the change.

**We explicitly accept delayed financial visibility as a consequence of separating
durable storage in usage_inbox from accounting.** Ingestion into usage_inbox can continue during a worker outage,
subject to PostgreSQL availability and storage capacity. Accounting keeps its
atomic financial transaction and can recover from the backlog. The assignment
also anticipates platform lag and requires billing all measured usage above the
limit. The platform owns the reaction to the status.

This rationale does not establish that the additional delay is negligible.
The current design provides neither a fixed maximum delay nor a guarantee that
actual platform spend never exceeds the limit. A worker outage or quarantine can
extend the delay until recovery. `pending_events` and `processing_errors` are
customer-wide counts, not month-specific completeness indicators; zero cannot
prove that no measurements remain on the platform. `state_version` is a revision
counter, not a freshness timestamp. Raising the stored limit takes effect after
its command commits; current-month reads select the new UTC month without waiting
for the worker.

### What to measure

- Measure platform measurement-to-usage_inbox row, usage_inbox row-to-accounting, and
  accounting-to-platform-observation separately, with p95/p99 and the oldest
  pending age. Include normal load, bursts, large customers, a stopped worker,
  and backlog recovery.
- Record gross spend accumulated between actual threshold crossing and platform
  reaction, including cases where the observed status was below the limit.
  At an approximately steady spend rate `r` USD/s and reaction delay `d` seconds,
  `r * d` USD is an estimate; bursts need direct measurement.
- Correlate stable event identities and platform observations. Add instrumentation
  where the required timestamps are unavailable. A usage interval is not the
  sender's delivery timestamp; cross-process measurements must account for clock
  differences and retries.

### When to act

Agree an acceptable status age, overspend impact, recovery time, and platform
behavior during stale or unavailable status before evaluating the results.
No numeric targets have been established by this prototype. If results exceed
the agreed targets, identify the dominant delay and adjust the corresponding
component: measurement/reporting cadence, worker throughput and work allocation,
poll cadence, or durable notifications through a transactional outbox.

Notifications address accounting-to-observation delay; they do not remove
undelivered input or an accounting backlog. If a product requires a strict
non-exceedable budget, evaluate a different control protocol, such as reserving
budget before work starts, and verify its failure behavior with the platform.

Implementation: [durable usage_inbox row](../../internal/httpapi/usage.go),
[limit reads and changes](../../internal/postgres/limits.go), and
[transactional accounting](../../internal/postgres/processor.go).

## L2: accepted usage on a later invoice

### Behavior and rationale

The [closing contract](accounting-rules.md#transaction-and-closing-contract)
selects already committed groups under the customer account lock. It does not
drain the inbox or wait for pending and quarantined input. Consequently, an event
received before closing but accounted afterward can appear on a later invoice.
It retains its consumption-time price and original-month gross spend, while
credit follows the balance and serialized order at accounting time.

This consciously extends a mechanism already necessary for measurements delivered
by the platform after an invoice is issued. It keeps closing independent of worker
availability and backlog size, and preserves immutable invoices. The working
assumption is that ordinary operation adds relatively few locally delayed events
to the unavoidable late-delivery cases. **That assumption is unverified.** A long
outage, overload, or unreleased quarantine can create a material deferral.

### What to measure

Distinguish events received after closure from events received before closure but
accounted afterward. For both populations record event count, exact gross value,
share of monthly usage, affected customers, time to invoice inclusion, and repeated
deferrals. Include credit-allocation effects when processing crosses a later
credit grant. Count alone is insufficient: a few expensive events can have a
large customer impact.

Reconcile input identities with exactly one accounting effect and eventual invoice
inclusion. Stored usage_inbox row and processing times alone do not establish the closing
lock cutoff precisely; investigation needs suitable closure/cutoff evidence.
Quarantined valid usage requires explicit recovery, not an assumption of automatic
eventual processing.

### When to act

Agree acceptable deferred value, frequency, and delay with billing/product owners.
Review backlog and quarantine before invoice runs. If measured impact is material,
address its cause and consider later invoice generation, a bounded waiting window,
or an operator pause during incidents. Waiting can reduce local deferral but cannot
prove the platform has delivered every measurement. Preserve historical prices,
atomic credit accounting, and invoice immutability when changing the policy.

Implementation: [invoice closing](../../internal/postgres/invoices.go),
[billing-month routing](../../internal/accounting/calendar.go), and
[accounting](../../internal/postgres/processor.go).

## L3: rounding across groups and invoices

Exact usage and credit remain in whole microcents. Invoice presentation rounds
cumulative gross and net amounts half-up per price/month group; displayed credit
is their cent difference. There is no rounding-remainder carry between groups or
invoices. See the [rounding contract](accounting-rules.md#rounding-and-invoice-presentation).

With no credit, two half-cent charges in one compatible group produce one cent.
Split across two invoices, they produce one cent each. The exact gross value is
unchanged. This explicit policy keeps each invoice self-contained and reproduces
the assignment's required example, whose general remainder policy is unspecified.

Measure the difference between actual invoice cents and a clearly stated comparison
policy, separately identifying extra boundaries caused by L2. Include gross, net,
and displayed credit; do not confuse exact ledger debits with cent presentation.
If the difference is material or product requirements demand totals independent
of delivery timing, design an audited remainder-carry policy and verify rounding
boundaries, credit, retries, and recovery without rewriting issued invoices.

Implementation: [exact calculations](../../internal/accounting/money.go) and
[invoice presentation](../../internal/billing/invoice_lines.go).

## Recording an investigation

For each item, record the code revision, load and failure conditions, observation
window, data completeness, agreed targets, results, and decision owner. State
whether the evidence supports retaining the policy or requires a mitigation,
and verify the mitigation under the same scenarios. Missing observations remain
open limitations; successful functional tests alone do not establish acceptable
operating impact.

Other implementation boundaries and production work, including usage_inbox row segmentation,
processing-order credit, explicit invoice invocation, retention, and backup recovery,
remain described in the [implementation overview](submission-overview.md#remaining-limits-and-improvements)
and [capacity assessment](capacity-and-measurements.md).
