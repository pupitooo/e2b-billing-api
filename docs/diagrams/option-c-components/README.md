# Option C components and interfaces

Option C is the implemented Go API, PostgreSQL inbox and separate Go accounting worker. This diagram shows assignment components and interfaces, not E2B production infrastructure. Financial commands, historical accounting, spend-status reads and monthly invoice publication are implemented.

![Option C components and interfaces](option-c-components.png)

[Editable Mermaid source](option-c-components.mmd) · [Architecture comparison](../../brainstorming/architecture-options.md) · [Submission overview](../../architecture/submission-overview.md).

## Processes and storage

The platform owns metering, a durable sender buffer and the reaction to a spend limit. The assignment simulator runs separately and persists stable measurements before delivery. The Go API and accounting worker always run in separate processes and Compose services, sharing one PostgreSQL database. There is no API-to-worker HTTP call; the inbox supplies their asynchronous boundary.

The two storage blocks are logical groups of tables in that database. The inbox holds receipt identity, original measurement, first receipt time and processing state. Financial tables hold customers, dated prices, recurring purchases, exact credit history, gross monthly spend, closed months, immutable invoices and frozen groups. The [implemented ERD](../../architecture/billing-model.md#implemented-postgresql-schema-erd-migrations-001009) describes all 16 tables after migrations 001–009.

## Receipt and accounting transactions

1. The platform persists a measurement with stable `(source, event_id)` and retains it until acknowledged. A timeout or lost reply causes a retry with identical content.
2. The API validates the batch and opens **T1**. New receipts and content comparisons commit atomically; conflicting content rolls back the whole batch.
3. After T1 commits, the API returns **202** and `{"status":"accepted"}`. The response acknowledges the whole batch without enumerating identities. It does not promise completed accounting.
4. The worker selects the earliest pending candidate, opens **T2**, locks the customer account and catalog, then rechecks and locks the pending receipt. It resolves historical prices and commits rating, group totals, credit, gross spend, state version and completion together. Concurrent workers can select the same candidate; the account lock and recheck prevent duplicate effects. No `SKIP LOCKED` claim or separately committed processing lease is used.
5. Database failure rolls back T2 for retry. Unsupported input commits quarantine without financial effects. Missing valid prices log a P0 incident only after quarantine commits; recovery requires an investigated catalog repair and explicit release.

Queries expose processed state plus pending/error counts. These counts describe known billing input, not measurements still buffered on the platform or a guaranteed latency bound.

## HTTP interfaces

Arrows show request/response or SQL interactions, not server push. Actor roles describe ownership; authentication is outside the assignment. Fields, statuses and retries are defined in [OpenAPI](../../api/openapi.yaml).

| Caller | Interface | Result |
| --- | --- | --- |
| Platform sender | `POST /usage/batches` | Atomically persist a batch; fixed acknowledgement after commit, identical retry preserved, changed content rejected. |
| Platform control | `GET /customers/{customer_id}/limit-status` | Current UTC month's processed gross usage, limit, reached status, state version and pending/error counts. |
| Platform or reviewer | `GET /customers/{customer_id}/months/{month}/limit-status` | Explicit original usage month with the current limit configuration. |
| Customer | `POST /customers/{customer_id}/spend-limit` | Set or remove the monthly limit using a stable operation identity. |
| Customer | `POST /customers/{customer_id}/addons` | Purchase a recurring add-on with its price snapshot. |
| Customer | `GET /customers/{customer_id}/credit` | Remaining exact credit and processing progress. |
| Customer or E2B | `GET /customers/{customer_id}/invoices/{month}` | Immutable issued invoice. |
| E2B administrator | `POST /customers/{customer_id}/credits` | Grant usage-only credit once per operation identity. |
| E2B administrator | `POST /prices` | Append a dated version; a new ordinary version cannot backdate activation. |
| Closing operator | `POST /customers/{customer_id}/invoices` | Issue the explicit `YYYY-MM` month from already processed groups. |

T1 uses the inbox; T2 uses both inbox and financial tables. API financial commands and invoice publication own their own account-locked transactions. Ingestion does not acquire the financial account lock. API reads inspect financial state and pending/error counts; the first invoice also checks whether earlier accepted usage would be skipped.

## Closing and spend status

Closing holds the account lock throughout one publication transaction, including validation, presentation and freezing. It never invokes the receipt processor or waits for pending target-month input. The first invoice cannot skip earlier known usage; subsequent new invoices require the preceding calendar month closed. Only an ended UTC month can close. Retrying a committed invoice returns its original snapshot and number.

An in-flight worker holding the account lock completes before closing acquires it; its committed groups are included. A worker accounting after closure routes to an eligible open billing month without changing the invoice. Consumption-time prices and original-month gross spend remain intact. Pending and quarantined target-month receipts are not billed until successfully processed; different invoice months retain separate rounding groups.

The platform polls limit status independently of delivery, including after a limit change or month rollover. Credit and add-ons do not change the limit's gross-usage basis. Polling avoids coupling deployments to a notification transport. Freshness targets, outage behaviour and any versioned push notifications remain operational agreements or extensions.

## Diagram artifacts

The PNG preview and native Mermaid source are committed together. Reading the diagram requires no local renderer; generation tooling stays private and outside the submitted repository.
