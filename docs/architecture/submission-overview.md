# Billing service architecture and assignment coverage

The assignment's billing flow is implemented in Go and PostgreSQL: durable storage in usage_inbox, historical pricing, usage-only credit, recurring add-ons, gross monthly spend limits and immutable numbered invoices. The platform is represented by a separate, restartable Go simulator. The [public assignment workflow](../simulator/billing-scenarios.md#run-the-complete-assignment) reproduces the required USD 20.00, USD 20.00 and USD 18.17 invoices, with Acme credit balances of USD 13.00 and USD 7.00 at the specified checkpoints.

## usage_inbox row and accounting

The platform persists each measurement outside the sandbox lifecycle before sending `POST /usage/batches`. Stable `(source, event_id)` identities and unchanged content make retries safe. The API validates a whole batch and commits it atomically to `usage_inbox` before returning `202` with `{"status":"accepted"}`. Identical retries preserve the original usage_inbox row; changed content conflicts. The producer retains unacknowledged measurements through outages or lost replies.

A separate worker polls the committed inbox. Each processing transaction locks the customer account first, rechecks the pending usage_inbox row, selects its consumption-time price, allocates exact credit and updates gross spend. Rating, group totals, credit history and balance, state version and usage_inbox row completion commit together. A rollback cannot leave a partial charge. Unsupported input is quarantined without financial effects; missing valid prices produce a P0 incident after quarantine commits. Operators repair historical prices through [controlled SQL](../guides/accounting-recovery.md#controlled-historical-price-repair), then explicitly release the investigated usage_inbox row.

## Interfaces and ownership

Roles identify callers and responsibilities; the assignment has no authentication layer. The [OpenAPI specification](../api/openapi.yaml) defines fields, examples, statuses and retry semantics for every endpoint.

| Caller | Interface | Responsibility |
| --- | --- | --- |
| Platform sender | `POST /usage/batches` | Deliver sandbox measurements; retain and retry unacknowledged input. |
| Platform control | `GET /customers/{customer_id}/limit-status` | Poll the current UTC month's gross spend and reached status. Decide the platform's reaction. |
| Platform or reviewer | `GET /customers/{customer_id}/months/{month}/limit-status` | Inspect an explicit original usage month. |
| E2B administrator | `POST /prices`, `POST /customers/{customer_id}/credits` | Append dated prices and grant usage credit with stable command identities. |
| Customer | `POST /customers/{customer_id}/addons`, `POST /customers/{customer_id}/addons/cancel`, `POST /customers/{customer_id}/spend-limit` | Buy or cancel a recurring add-on and set or remove a gross monthly usage limit. |
| Customer | `GET /customers/{customer_id}/credit` | Read remaining exact credit and accounting progress. |
| Closing operator | `POST /customers/{customer_id}/invoices` | Explicitly issue one invoice for an ended UTC month in valid chronological order. |
| Closing operator | `POST /customers/{customer_id}/invoices/catch-up` | Atomically recover up to 12 missing completed months, preserving one invoice per month and the command response on retry. |
| Customer or E2B | `GET /customers/{customer_id}/invoices/{month}` | Read the immutable buyer and financial snapshot. |
| Local operations | `GET /healthz` | Check HTTP availability; this does not verify database health. |

Polling keeps platform deployment independent of billing and needs no notification transport. Gross usage counts before credit and excludes add-ons; reaching the limit never stops accounting. Reads expose pending/error counts, but cannot observe measurements still buffered on the platform. Poll cadence, tolerated lag and outage behaviour need an operational agreement.

## Tools and transaction boundaries

Go is the fixed implementation language. Standard HTTP and context cancellation support small independent API, worker and simulator executables; arbitrary-precision integer calculations avoid floating-point money. Pure calculations stay separate from transport and storage.

PostgreSQL provides durable input, exact integer-valued `numeric`, constraints, append-only history and per-customer row locks. It lets financial effects and completion share one transaction, although ingestion and accounting compete for the same database resources. The schema has **17 tables after migrations 001–013**; [the billing model and ERD](billing-model.md#implemented-postgresql-schema-erd-migrations-001013) describe them. Versioned migrations and version records commit together under an advisory lock.

Docker Compose and Make provide one reproducible local runtime for PostgreSQL, API, worker, simulator, the Markdown documentation portal and Scalar API reference. Scalar reads the same OpenAPI contract and provides browser requests. The simulator's durable state makes repeats, outages and lost committed responses reviewable without a real sandbox platform.

Invoice issuance uses one customer-account-locked transaction and includes only already processed groups. It never drains pending usage. Later accounting, including previously accepted input, retains historical prices and original-month gross spend but routes to an eligible open billing month. Publication freezes groups, snapshots the buyer and lines, and advances numbering atomically. Retries return the committed invoice. Separate billing months can introduce separate rounding boundaries.

## Remaining limits and improvements

The [limitations register](limitations.md) explicitly documents delayed limit status,
accepted usage appearing on later invoices, and cross-invoice rounding. Their
operating impact remains to be measured; it gives the rationale, investigation
criteria, and possible mitigations without claiming established production targets.

The required worked example and financial interfaces are implemented. Current limits are explicit: usage_inbox rows must fit one UTC month and applicable price version; credit allocation follows serialized processing order; invoices are explicitly invoked; and retained local volumes cover restart, not storage destruction. New ordinary prices activate at midnight UTC on today or a future date and protect rated history. Add-on purchases use UTC calendar dates. Credit audit times are generated by the backend. Fixed October/November 2026 workflows require ended months; [integration tests](../../tests/inbox/billing_simulator_test.go) supply declared server clocks for earlier review, including separate insertion times for scheduled prices. HTTP callers cannot override the clock. A [fixed-clock live demo](../guides/demo-console.md#fixed-clock-invoice-demo) selects shared API/worker time through startup configuration and exposes only static portal metadata to the GUI.

Automatic interval segmentation would require additional measurement detail or an agreed allocation rule. Corrections need audited financial differences without modifying issued invoices. Scheduling and invoice delivery need durable work and retry policy. Replay-safe retention, backups, production observability and company log forwarding need deployment contracts.

For approximately 50_000 customers and billions of monthly measurements, the [scaling proposal](../brainstorming/architecture-options.md#scaling-to-billions-of-records) separates raw event ingestion, archival/aggregation and transactional finance. It preserves stable identities, price and month boundaries, account ownership and the processed-usage invoice cutoff. Kafka, analytical storage and customer sharding are proposed extensions. Workload calculations and short local samples do not establish production capacity.

The [backend E2E suite](../guides/e2e-testing.md) runs the public scenarios
against separate API, worker and PostgreSQL services in CI, including configuration
clock changes, outages, rollback and restart. The [manual walkthrough](../guides/e2e-walkthrough.md)
lets you inspect each intermediate request and persisted result. Use the
[acceptance procedure](../guides/final-acceptance-testing.md) for detailed SQL,
invoice tables and human presentation/review evidence.

## Runtime boundaries

Each API or worker process opens one PostgreSQL pool and constructs one
[`postgres.Store`](../../internal/postgres/store.go). Its configuration supplies
one clock and one rollback timeout to usage ingestion and financial operations.
The API passes `E2B_API_ROLLBACK_TIMEOUT`; the worker uses the default five-second
cleanup budget. Rollback uses a context independent of request cancellation.
arrival times remain explicitly supplied by the HTTP handler.
`internal/billingclock` supplies the common real or configured fixed business
provider to both handler and store. Worker completion and debit audit share one
sampled transaction time; lifecycle deadlines and heartbeats stay real.

The HTTP handler requires a complete store at construction. Missing storage is a
startup error, including a typed nil dependency; all implemented routes are
registered together. A database failure after startup returns the documented
retryable response rather than removing routes. Health reports HTTP server
availability and remains available during a storage outage.

Persistence methods remain grouped by operation in `internal/postgres`. Financial
inputs, validation, and pure invoice presentation stay in `internal/billing`;
price selection and exact monetary calculations stay in `internal/accounting`.
The store preserves the existing usage_inbox row and financial transaction boundaries.
