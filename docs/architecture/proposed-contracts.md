# Proposed contracts and open decisions

This list separates confirmed requirements awaiting implementation from proposed
details and undecided extensions. It complements the
[implemented contracts](implemented-contracts.md). A recommendation below is not an adopted
runtime policy. An invoice scheduler, customer-specific issue-day settings and
delivery integration are not implemented.

## Confirmed requirements awaiting implementation

| ID | Requirement | Implemented foundation and remaining work |
| --- | --- | --- |
| R1 | Monthly invoices must be issued automatically without routine manual input. | Immutable issuance and bounded manual catch-up exist. Automatic discovery, invocation, retries and outage recovery are missing. Timing and operating targets remain open; see [automatic issuance](#automatic-invoice-issuance). |
| R2 | Billing logs must be available in the shared company logging system before production deployment. | The worker emits JSON to standard output and the API emits plain text. Company collection, access, retention and incident integration are pending; the platform has not been selected. |
| R3 | Production invoice integration must retain the external document reference. | Internal customer/month identity and snapshot exist. Provider selection, integration metadata, external creation and reconciliation remain pending; see the [external reference design](usage-to-invoice.md#production-note-external-invoice-reference-planned). |

These requirements supplement the prototype; they do not add payment processing,
tax calculation or authentication to the assignment. Delivery, customer-specific
issue days and a preview are separate proposed extensions.

## Automatic invoice issuance

**Confirmed requirement:** ordinary monthly issuance must not require an operator
to select each customer/month, submit a command, or manually restart a missed run.
An exceptional incident can still require intervention; automation must expose
failure rather than declare an unissued invoice successful.

**Existing implementation:** `CloseMonth` issues one completed month atomically;
`CatchUpInvoices` recovers at most 12 missing months in one transaction. Both
serialize with financial writes through the customer account lock. The API,
accounting worker and Compose configuration contain no invoice scheduler.
The same catch-up key replays its original range even after later closures.

**Recommended design, pending selection:**

1. Run a separate invoice scheduler that discovers due customer/month work at
   startup and periodically. Persist sufficient progress and retry state to
   resume after a crash. A timer waking once at the month boundary is insufficient:
   missing that wakeup must not lose a month's work.
2. Make the previous UTC calendar month eligible from 00:00 UTC on the first day
   of the next month. This is a proposed eligibility time, not a promise that all
   customer invoices commit at that instant. Later fixed issue days remain an
   alternative; the financial period stays a whole UTC month.
3. Reconcile due work with committed invoices and closures. Recover missing months
   in increasing order, including empty and active add-on months. Use bounded
   transactions and continue automatically across gaps longer than 12 months.
   Separate monthly commits are a proposed scheduler boundary, distinct from the
   existing all-or-nothing manual catch-up command.
4. Use stable customer/month work identities. Repeat attempts must reuse the
   committed snapshot and number without another credit debit. If the scheduler
   calls manual catch-up, persist its target and command key; do not reuse a
   successful key while expecting a newly extended response range.
5. Allow multiple scheduler instances and concurrent manual commands without
   duplicate publication. Preserve the account lock and invoice uniqueness;
   choose work reservation, expired-owner recovery and transaction deadlines
   explicitly. A work marker cannot replace the final atomic publication.
6. Continue other customers after an isolated failure. Retry transient failures
   with a bounded delay, preserve unresolved work, and expose failed or overdue
   customer/months for alerting. A scheduler heartbeat alone proves neither
   successful issuance nor recovery of all missed months.
7. Keep delivery and external provider calls outside the financial publication
   transaction. If delivery is added, record durable delivery intent atomically
   with publication and retry it independently. Delivery failure must not create
   a second internal invoice or silently change the issued snapshot.

### Decisions required before implementation

| Decision | Recommendation | Still to select |
| --- | --- | --- |
| Issue day and UTC time | First day, eligible from 00:00 UTC. | First day or a later fixed day; whether customer preferences are needed. |
| First automatic invoice | Discover known historical obligations as existing catch-up does. | Whether a customer with no usage, purchases or previous invoices starts in its registration month or the first completed month considered by the scheduler. There is no explicit first-month setting. |
| Due-work discovery and execution | Separate scheduler with startup reconciliation and periodic scans. | Scan interval, customer pagination, persistent work representation, ownership and transaction deadlines. |
| Retry and outage recovery | Automatically continue chronological work; isolate customer failures. | Retry delay/backoff, concurrency, fair allocation, incident thresholds and treatment of permanent failures. |
| Completion target | Observe due, issued, failed and oldest overdue months. | Maximum acceptable issuance delay and backlog recovery time; no numeric SLA is selected. |
| Delivery | Keep generation and delivery independent. | Whether delivery is in scope, its channel/provider, retry identity and success evidence. |

The existing processed-group cutoff is already selected: pending and quarantined
usage does not hold up an invoice. Changing the issuance day can reduce some
deferral but cannot guarantee complete metering. See the
[scheduling alternatives](../brainstorming/invoice-scheduling.md) and
[deferred-invoicing limitation](limitations.md#l2-accepted-usage-on-a-later-invoice).

### Verification required for a future scheduler

Existing closing and catch-up tests cover ended-month boundaries, rollback,
concurrent commands, frozen retry responses, active add-on months, empty months
and the 12-month bound. They verify the financial foundation, not automation.
See [closing scenarios](../../tests/inbox/invoices_test.go),
[catch-up scenarios](../../tests/inbox/invoice_catchup_test.go) and
[failure and concurrency scenarios](../../tests/inbox/invoice_catchup_boundaries_test.go).

Future scenarios must show automatic issuance with no GUI/API operator action;
startup after a missed run; crash before and after publication; lost committed
response; gaps beyond 12 months; multiple schedulers and competing manual closes;
one failed customer while another progresses; UTC month boundaries, leap years
and the supported timestamp range; and new customers with no prior obligations.
Each scenario must expose literal invoice numbers, amounts and final work state.
Preserve credit, add-on fees, snapshot immutability and pending-usage routing.
Test delivery retries separately if that extension is selected.

## Other open contracts

These choices are not all prerequisites for the prototype or the scheduler.
Resolve each when implementing the corresponding responsibility; retain existing
behavior until a change is selected and verified.

| ID | Topic | Existing boundary | Decision still needed |
| --- | --- | --- | --- |
| O1 | Limit-status freshness and platform outages | Status uses committed original-month gross usage; the platform owns its response to a limit. | Poll cadence, tolerated age and overspend, stale/unavailable-status behavior and alerts. [Delayed status](limitations.md#l1-delayed-spend-limit-status). |
| O2 | Metering completeness and first durable ownership | Billing acknowledges committed input; transport retries cannot recreate never-recorded units. | Metric meaning, final-interval recovery, authoritative counters, owner of a missing measurement and behavior when the platform buffer cannot persist. [Usage boundary](usage-to-invoice.md#3-usage-events-what-was-consumed). |
| O3 | Retention, old replay and storage-loss recovery | Online identities and audit history are retained; local volumes cover restart. | Accepted outage/replay horizon, archive and deduplication retention, backup/restore verification, RPO and RTO. Do not delete an identity while its replay can be charged again. [Capacity](capacity-and-measurements.md). |
| O4 | Corrections and financial reconciliation | Changed content under an accepted event identity conflicts; issued invoices stay immutable. | Positive/negative correction linkage, historical price and month, credit restoration, limit adjustment, adjustment documents and reconciliation ownership. No automatic correction exists. |
| O5 | Interval segmentation | A receipt must fit one applicable price version and UTC month. | Platform segmentation or an explicit allocation rule; units cannot be inferred from duration without agreement. [Pricing](accounting-rules.md#historical-prices-and-groups). |
| O6 | Credit allocation and rounding alternatives | Credit follows serialized accounting order; cumulative groups round half-up with no remainder carry. | Whether future requirements need consumption-order allocation or audited remainder carry. Both change financial outcomes and require a separate decision. [Rounding impact](limitations.md#l3-rounding-across-groups-and-invoices). |
| O7 | Add-on extensions and platform access | Purchase and inclusive cancellation months are fully charged; purchased prices are retained. | Quantities, proration, existing-subscription price changes and coordination with platform entitlements. These are not current billing rules. [Add-ons](accounting-rules.md#utc-months-limits-add-ons-and-late-usage). |
| O8 | Configurable price denominator | Whole cents per 1_000_000 units produce whole microcents. | Whether to add a per-price denominator and what precision to permit; preserve seed values and historical charges. [Rating proposal](usage-to-invoice.md#4-rating-the-cost-under-the-historical-price). |
| O9 | Catalog concurrency and accounting scale | Catalog and customer locking preserve historical-price and financial consistency. | Any lock relaxation, aggregation, work reservation, broker or sharding, based on measured contention and an explicit recovery contract. [Scaling options](../brainstorming/architecture-options.md#scaling-to-billions-of-records). |

Start with R1's timing, customer eligibility and recovery targets. Resolve delivery
and external integration separately. Agree production freshness, durability and
observability targets before promising them; architecture alternatives alone do
not select their values.
