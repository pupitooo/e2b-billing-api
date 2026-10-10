# Billing service architecture and assignment coverage

The assignment's billing flow is implemented in Go and PostgreSQL: durable usage receipt, historical pricing, usage-only credit, recurring add-ons, gross monthly spend limits and immutable numbered invoices. The platform is represented by a separate, restartable Go simulator. The [public assignment workflow](../simulator/billing-scenarios.md#run-the-complete-assignment) reproduces the required USD 20.00, USD 20.00 and USD 18.17 invoices, with Acme credit balances of USD 13.00 and USD 7.00 at the specified checkpoints.

## Usage receipt and accounting

The platform persists each measurement outside the sandbox lifecycle before sending `POST /usage/batches`. Stable `(source, event_id)` identities and unchanged content make retries safe. The API validates a whole batch and commits it atomically to `usage_inbox` before returning `202` with `{"status":"accepted"}`. Identical retries preserve the original receipt; changed content conflicts. The producer retains unacknowledged measurements through outages or lost replies.

A separate worker polls the committed inbox. Each receipt's transaction locks the customer account first, rechecks the pending receipt, selects its consumption-time price, allocates exact credit and updates gross spend. Rating, group totals, credit history and balance, state version and receipt completion commit together. A rollback cannot leave a partial charge. Unsupported input is quarantined without financial effects; missing valid prices produce a P0 incident after quarantine commits. Operators repair historical prices through [controlled SQL](../../README.md#controlled-historical-price-repair), then explicitly release the investigated receipt.

## Interfaces and ownership

Roles identify callers and responsibilities; the assignment has no authentication layer. The [OpenAPI specification](../api/openapi.yaml) defines fields, examples, statuses and retry semantics for every endpoint.

| Caller | Interface | Responsibility |
| --- | --- | --- |
| Platform sender | `POST /usage/batches` | Deliver sandbox measurements; retain and retry unacknowledged input. |
| Platform control | `GET /customers/{customer_id}/limit-status` | Poll the current UTC month's gross spend and reached status. Decide the platform's reaction. |
| Platform or reviewer | `GET /customers/{customer_id}/months/{month}/limit-status` | Inspect an explicit original usage month. |
| E2B administrator | `POST /prices`, `POST /customers/{customer_id}/credits` | Append dated prices and grant usage credit with stable command identities. |
| Customer | `POST /customers/{customer_id}/addons`, `POST /customers/{customer_id}/spend-limit` | Buy a recurring add-on and set or remove a gross monthly usage limit. |
| Customer | `GET /customers/{customer_id}/credit` | Read remaining exact credit and accounting progress. |
| Closing operator | `POST /customers/{customer_id}/invoices` | Explicitly issue one invoice for an ended UTC month in valid chronological order. |
| Customer or E2B | `GET /customers/{customer_id}/invoices/{month}` | Read the immutable buyer and financial snapshot. |
| Local operations | `GET /healthz` | Check HTTP availability; this does not verify database health. |

Polling keeps platform deployment independent of billing and needs no notification transport. Gross usage counts before credit and excludes add-ons; reaching the limit never stops accounting. Reads expose pending/error counts, but cannot observe measurements still buffered on the platform. Poll cadence, tolerated lag and outage behaviour need an operational agreement.

## Tools and transaction boundaries

Go is the fixed implementation language. Standard HTTP and context cancellation support small independent API, worker and simulator executables; arbitrary-precision integer calculations avoid floating-point money. Pure calculations stay separate from transport and storage.

PostgreSQL provides durable input, exact integer-valued `numeric`, constraints, append-only history and per-customer row locks. It lets financial effects and completion share one transaction, although ingestion and accounting compete for the same database resources. The schema has **16 tables after migrations 001–009**; [the billing model and ERD](billing-model.md#implemented-postgresql-schema-erd-migrations-001009) describe them. Versioned migrations and version records commit together under an advisory lock.

Docker Compose and Make provide one reproducible local runtime for PostgreSQL, API, worker, simulator and Scalar documentation. Scalar reads the same OpenAPI contract and provides browser requests. The simulator's durable state makes repeats, outages and lost committed responses reviewable without a real sandbox platform.

Invoice issuance uses one customer-account-locked transaction and includes only already processed groups. It never drains pending usage. Later accounting, including previously accepted input, retains historical prices and original-month gross spend but routes to an eligible open billing month. Publication freezes groups, snapshots the buyer and lines, and advances numbering atomically. Retries return the committed invoice. Separate billing months can introduce separate rounding boundaries.

## Remaining limits and improvements

The required worked example and financial interfaces are implemented. Current limits are explicit: receipts must fit one UTC month and applicable price version; credit allocation follows serialized processing order; invoices are explicitly invoked; and retained local volumes cover restart, not storage destruction. New ordinary prices cannot backdate activation. Fixed October/November 2026 workflows require ended months; [integration tests](../../tests/inbox/billing_simulator_test.go) supply declared server clocks for earlier review, including separate insertion times for scheduled prices. HTTP callers cannot override the clock.

Automatic interval segmentation would require additional measurement detail or an agreed allocation rule. Corrections need audited financial differences without modifying issued invoices. Scheduling and invoice delivery need durable work and retry policy. Replay-safe retention, backups, production observability and company log forwarding need deployment contracts.

For approximately 50_000 customers and billions of monthly measurements, the [scaling proposal](../brainstorming/architecture-options.md#scaling-to-billions-of-records) separates raw receipt, archival/aggregation and transactional finance. It preserves stable identities, price and month boundaries, account ownership and the processed-usage invoice cutoff. Kafka, analytical storage and customer sharding are proposed extensions. Workload calculations and short local samples do not establish production capacity.

Use the [acceptance procedure](../guides/final-acceptance-testing.md) to record one revision's test results, expected invoices, persisted balances and restart behaviour.
