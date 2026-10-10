# Billing architecture options

Option C is implemented: a Go ingestion API commits measurements to a PostgreSQL inbox, a separate Go worker accounts for them, and financial APIs expose prices, credit, add-ons, limits and immutable monthly invoices. The platform simulator reproduces the assignment. The [submission overview](../architecture/submission-overview.md) summarizes interfaces, tools and remaining limits; this comparison explains alternatives and future growth.

Go is fixed for every option. Storage, transport and processing arrangements vary. Options A, B and D are alternatives, not descriptions of the current implementation or E2B's production infrastructure. Payment processing, tax calculation and authentication are outside the assignment.

## Contents

- [Options at a glance](#options-at-a-glance)
- [Option A: synchronous Go service with SQLite](#option-a-synchronous-go-service-with-sqlite)
- [Option B: synchronous Go service with PostgreSQL](#option-b-synchronous-go-service-with-postgresql)
- [Option C: PostgreSQL inbox and asynchronous Go worker](#option-c-postgresql-inbox-and-asynchronous-go-worker)
- [Option D: Kafka ingestion and separate accounting pipeline](#option-d-kafka-ingestion-and-separate-accounting-pipeline)
- [Why option C was selected](#why-option-c-was-selected)
- [Workload assumptions for further analysis](#workload-assumptions-for-further-analysis)
- [Implemented correctness and remaining limits](#implemented-correctness-and-remaining-limits)
- [TODO: freshness, durability, and operations](#todo-freshness-durability-and-operations)
- [Scaling to billions of records](#scaling-to-billions-of-records)

## Options at a glance

| Option | Storage and processing | Usage acknowledgement | Main tradeoff |
| --- | --- | --- | --- |
| A | Go API, synchronous accounting, SQLite | After the financial transaction commits | Minimal setup; database writes share one writer. |
| B | Go API, synchronous accounting, PostgreSQL | After the financial transaction commits | One accounting path; ingestion waits for financial work. |
| C — selected | Go API, PostgreSQL inbox, asynchronous Go worker | After the inbox transaction commits | Recoverable pending work; accounting and spend status can lag. |
| D | Go ingress, Kafka, aggregation, PostgreSQL accounting | After the configured durable broker acknowledgement | Separate ingestion and processing; more services and recovery boundaries. |

Every option requires stable `(source, event_id)` identities, durable sender storage outside the sandbox lifecycle, and retries until the matching acknowledgement. The [system contracts](../../README.md#system-contracts) record the required identities, delivery guarantees, and processing boundaries. Storage must preserve each measurement's original consumption time.

## Option A: synchronous Go service with SQLite

The API validates, deduplicates, prices usage, allocates credit, and updates spend in one SQLite transaction before responding. SQLite stores receipt identities, accounting records, and immutable invoices on the billing host. This keeps local setup small.

**Weak point:** an Acme usage write and a Cyberdyne invoice closing compete for the same database writer. Short transactions and bounded batches reduce waiting, at the cost of more batch coordination; spreading writers over hosts requires a storage change. SQLite WAL permits only one writer at a time and requires participating processes on the same host. [SQLite WAL concurrency](https://www.sqlite.org/wal.html#concurrency).

![Option A: building blocks and interfaces](../diagrams/option-a-components/option-a-components.png)

[Native Mermaid source](../diagrams/option-a-components/option-a-components.mmd).

## Option B: synchronous Go service with PostgreSQL

The API performs ingestion and accounting in one PostgreSQL transaction. Receipt identity, usage charges, credit allocation, and spend state commit together; the response confirms that accounting has completed. Customer locks coordinate shared credit and closing while independent customers can proceed separately. [PostgreSQL row locks](https://www.postgresql.org/docs/18/explicit-locking.html#LOCKING-ROWS).

**Weak point:** a slow accounting transaction delays usage acknowledgement and spend-status updates. For example, a large Cyberdyne batch can keep its other financial operations waiting on the customer lock. Smaller batches and shorter transactions reduce that delay, but add coordination and do not remove the customer's shared-credit serialization.

![Option B: building blocks and interfaces](../diagrams/option-b-components/option-b-components.png)

[Native Mermaid source](../diagrams/option-b-components/option-b-components.mmd).

## Option C: PostgreSQL inbox and asynchronous Go worker

The ingestion API atomically commits validated measurements to `usage_inbox`, then returns `202` with `{"status":"accepted"}`. The worker processes one receipt per transaction, locking the customer account before rechecking and locking the pending receipt. Its financial effects and completion marker commit together. The API and worker are separate Go processes and Compose services sharing one PostgreSQL database; they can restart independently. The simulator retains stable measurements and retries after lost replies or outages.

Process isolation contains a worker crash and allows separate resource limits. It does not isolate shared database contention: workers can delay API writes through transactions or exhausted database capacity. The API has bounded admission, connection pools and deadlines; adding worker replicas still requires measured database headroom and does not remove contention over the earliest pending receipt.

**Weak point:** accepted input can be ahead of the financial state. If Cyberdyne's second example hour is waiting in the inbox, the platform can still see spend below its USD 15 limit. Limit and credit reads expose pending/error counts, but cannot see undelivered platform measurements. Closing snapshots only accounting committed before it acquires the customer lock; even previously accepted input can bill later. This keeps closing independent of backlog size, at the cost of incomplete current invoices, separate rounding groups and a freshness agreement. The shared database remains an availability and capacity dependency.

![Option C: building blocks and interfaces](../diagrams/option-c-components/option-c-components.png)

[Native Mermaid source](../diagrams/option-c-components/option-c-components.mmd).

## Option D: Kafka ingestion and separate accounting pipeline

A Go ingress publishes stable raw measurements to Kafka. An archive consumer preserves raw history; an aggregator prepares immutable deltas through a durable state store and outbox; accounting workers apply those deltas to PostgreSQL. The billing API serves financial commands and status. This arrangement provides separate places to buffer and distribute work.

**Weak point:** a worker can commit Acme's credit allocation and crash before recording its Kafka progress. Replaying the input must preserve the original identity and avoid another financial effect. Stable delta IDs and atomic accounting deduplication address this, with additional state, replay tests, retention management, and operating cost. Kafka transactions alone do not make an external PostgreSQL write atomic with consumer progress. [Kafka delivery semantics](https://kafka.apache.org/43/design/design/#message-delivery-semantics).

![Option D: building blocks and interfaces](../diagrams/option-d-components/option-d-components.png)

[Native Mermaid source](../diagrams/option-d-components/option-d-components.mmd).

## Why option C was selected

Option C keeps incoming measurements durable independently of the accounting worker, makes pending work visible and avoids operating a broker in the assignment implementation. PostgreSQL supplies receipt identity, exact financial storage and account locks in one local service. Separate API and worker lifecycles allow recovery without coupling receipt availability to a running worker.

C adds a second transaction and a gap between receipt and accounting. The inbox key and content comparison protect receipt retries; financial correctness requires accounting writes and completion to commit together. The source buffer retains unacknowledged measurements. Choosing C does not establish production capacity or adopt D as the eventual architecture.

## Workload assumptions for further analysis

The assignment asks the design to consider about 50_000 customers and potentially billions of monthly sandbox records. The implementation is not required to demonstrate that capacity. For these calculated examples, assume one metric, one record per active sandbox per minute, a constant average active-sandbox count, and a 30-day comparison period. Billing itself continues to use actual UTC calendar months.

| Illustrative profile | Average active sandboxes per customer | Records per second | Records per 30 days |
| --- | ---: | ---: | ---: |
| 50_000 customers | 10 | 8_333.3 | 21.6 billion |
| 50_000 customers | 100 | 83_333.3 | 216 billion |
| 50_000 customers | 1_000 | 833_333.3 | 2.16 trillion |

Calculated rate = customers × average active sandboxes × metrics ÷ 60. Monthly records = customers × average active sandboxes × metrics × 43_200. These counts are scenarios, not measured capacity; batching HTTP requests does not remove event identities or financial work.

## Implemented correctness and remaining limits

| Implemented contract | Detail and verification |
| --- | --- |
| Strict measurement validation and atomic receipt | [OpenAPI](../api/openapi.yaml) defines JSON, time, identity, body and batch limits. [HTTP tests](../../tests/api/usage_batches_test.go) and [repository tests](../../tests/inbox/usage_inbox_test.go) verify retries, conflicts, cancellation and rollback. |
| Exactly-once financial effects through retries | Account-first locking, pending-receipt recheck and one financial transaction are described in the [billing model](../architecture/billing-model.md#architectural-decisions-and-rationale); [accounting tests](../../tests/inbox/billing_test.go) cover concurrent workers and rollback. |
| Historical prices, exact credit and cumulative rounding | [Financial rules](../architecture/accounting-rules.md) distinguish consumption prices, ticks, group rounding and serialized credit allocation. [Primitive tests](../../internal/accounting/money_test.go) and [price tests](../../internal/accounting/pricing_test.go) cover boundaries and overflow. |
| Closing from processed usage, immutable invoices and numbering | The [closing contract](../architecture/accounting-rules.md#transaction-and-closing-contract) and [processed-closing tests](../../tests/inbox/processed_closing_test.go) cover pending input, both lock orders, month order, retries and separate rounding groups. |
| Assignment values and durable platform recovery | [Public workflows](../simulator/billing-scenarios.md) and [executable tests](../../tests/inbox/billing_simulator_test.go) reproduce invoices, balances, limits, lost committed replies and outages. |

Intervals must fit one applicable price version and one UTC month; ambiguous crossings are quarantined, not automatically split. Missing valid prices produce a P0 incident without charges. Historical catalog repair and explicit receipt release use the [operator procedure](../../README.md#controlled-historical-price-repair); ordinary price commands cannot backdate a new version. Closing never drains or repairs input, and pending/quarantined target-month usage can bill later.

Credit allocation follows serialized processing order. Splitting a compatible group preserves its cumulative amount; reordering around grants or closing can change credit timing or the billing group. No inbox cleanup is implemented: deduplication depends on retained receipt identities and content. Automatic interval segmentation, correction policy and replay-safe retention remain extensions.

## TODO: freshness, durability, and operations

- [ ] Agree a spend-status freshness target, polling cadence and platform behaviour when status is unavailable. Gross usage, month rollover, changed limits and pending/error visibility are implemented; undelivered measurements remain invisible.
- [ ] Agree production failure coverage, replay age, buffer capacity and recovery ownership. The simulator retains pending data through process restarts; volume loss, sustained overload and disaster recovery need separate guarantees.
- [ ] Plan database backup, restoration, and any replication required by the agreed failure coverage. A surviving local volume covers a restart, not destruction of its only storage copy.
- [ ] Define metrics and alerts for arrival rate, processing rate, oldest unfinished input, quarantine, lock waits, and storage growth; add reconciliation between received units and accounting effects.

## Scaling to billions of records

**Proposal:** keep C initially; move towards D when measured writes, storage or processing delay exceed agreed targets. These extensions are not implemented or adopted contracts.

Process large measurement volumes separately; send smaller accounting increments to the financial database:

| Layer | Purpose | Main benefit |
| --- | --- | --- |
| Queue, e.g. Kafka | Retain accepted measurements pending processing. | Absorb bursts and accounting outages. |
| History: object archive, optional analytical (OLAP) database | Archive originals; OLAP serves historical reports. | Keep large queries away from financial writes. |
| Transactional database, e.g. PostgreSQL | Maintain credit, charges, limits and invoices. | Commit related financial changes together. |

OLAP is optional; no engine is selected. A Go aggregator prepares increments; PostgreSQL remains the financial source of truth.

1. **Keep receipt available during accounting outages.** C already separates receipt and accounting. D's replicated queue can accept measurements while the financial database is unavailable. Confirm durable receipt, then account later. **Cost:** another service and a finite buffer that can fill during prolonged overload.

2. **Reduce financial writes by combining usage.** One customer's 100 sandboxes at the same CPU price can produce one minute's accounting increment instead of 100 writes. Keep originals for audit. Combine matching metric, price, UTC usage month and billing destination; preserve credit-grant order, exact money and rounding. **Cost:** waiting for a sum delays limit visibility.

3. **Keep restarts from changing charges.** C already prevents double charging. An aggregator must not place E2 in both sums A and B. Save consumed inputs with their immutable output, then commit each output's identity with its financial effects. **Cost:** durable aggregation state; Kafka alone cannot make external PostgreSQL writes atomic with consumer progress.

4. **Spread load and bound active storage.** Put Acme and Cyberdyne in separate PostgreSQL databases if needed, keeping one owner per account. Distribute original events by stable identity. Archive before deleting active history; retain identities throughout permitted replay. **Cost:** more stores and ownership coordination; a very large customer still needs aggregation.

5. **Keep invoice contents stable as the pipeline grows.** C closes only already processed groups under the customer account lock. D must preserve this cutoff through aggregation: unfinished input can appear on a later invoice, including already accepted usage. **Cost:** document this delay and separate rounding boundaries; platform limit reads must also expose processing progress.

6. **Prove recovery before switching.** At 1_000 measurements/s arriving and 1_000/s processed, an outage backlog never shrinks. Test bursts, large customers and recovery alongside normal operation. Monitor unfinished input, errors, archive lag and storage; catch up before retention expires. Compare old/new results without live financial effects, then transfer state and identities to one owner with a rollback plan. **Cost:** spare capacity and controlled migration.

Assuming 200 bytes/event, ten sandboxes/customer produce 4.32 TB per 30 days before indexes, WAL, replicas and backups. Retention must cover recovery and audit; [workload calculations](#workload-assumptions-for-further-analysis) do not establish capacity.

References checked on 10 October 2026: [Kafka delivery semantics](https://kafka.apache.org/43/design/design/#message-delivery-semantics), [PostgreSQL transactions](https://www.postgresql.org/docs/18/tutorial-transactions.html), and [OLAP workloads](https://clickhouse.com/docs/get-started/about/intro). The OLAP reference illustrates the role; it does not select a product.
