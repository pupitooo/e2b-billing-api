# Billing model and assignment seed data

The schema stores the assignment catalog, transactional accounting, financial command identities, closing cohorts, and immutable monthly invoices. [Shared financial rules and pure Go calculations](accounting-rules.md) define rating, exact credit, rounding, and UTC routing; the worker and public APIs implement those rules in `internal/billing`.

The [usage-to-invoice guide](usage-to-invoice.md) explains the accounting pipeline and the fields used at every stage. Migration 004 supports its credit-before-rounding order with exact tick balances, allocations, and ledger records.

## Foundation PostgreSQL schema (ERD, migrations 001–004)

This diagram shows all 12 tables and their SQL columns, primary keys, foreign keys, and relationship cardinalities after migrations `001` through `004`, including the runner's `schema_migrations` table. It uses the names and types from the migrations.

![Implemented PostgreSQL schema](../diagrams/implemented-data-model/implemented-data-model.png)

[Editable Mermaid source](../diagrams/implemented-data-model/implemented-data-model.mmd).

All columns are `NOT NULL` unless labeled nullable. `PK` and `FK` mark columns belonging to primary and foreign keys, including composite keys. Solid relationships include the referenced identity in the child's primary key; dashed relationships are other declared foreign keys. The Mermaid source records the composite unique constraints. `billing_ticks` is an exact `numeric` domain for non-negative finite integers; one cent is 1_000_000 ticks.

`usage_inbox` has no customer or metric foreign keys. Its optional one-to-one relationship with `usage_ratings` records whether an event has been assigned to a group; each rating references one `rated_usage_groups` row. The diagram shows database cardinalities, so a customer may have zero or one account-state row even though the seed creates one for each initial customer. Receipt matching and price ownership checks are enforced by triggers, as described below. The diagram is a foundation snapshot through migration 004. Migrations 005–007 add closure, command history, and invoice records described below; they are absent from this diagram.

## Logical data model (ERD)

The earlier ERD shows the proposed logical model, including planned invoice entities. Its names and attributes are conceptual; the implemented ERD above and the table summary below describe the schema created by these migrations.

![Proposed MVP logical data model](../diagrams/data-model/data-model.png)

[Editable Mermaid source](../diagrams/data-model/data-model.mmd).

`UsageReceipt` is implemented as `usage_inbox` plus the separate `usage_ratings` link. The inbox deliberately has no customer or metric foreign keys. Invoices use immutable JSON snapshots and frozen-group links rather than the separate line table proposed in the logical ERD.

## Tables and relationships

| Table | Purpose and relationships |
| --- | --- |
| `customers` | Customer identity, name, country, and current billing address. |
| `customer_billing_state` | One account row per customer with exact credit balance, optional spend limit, state version, and next invoice number. Financial writers serialize on this row. |
| `metrics` | Supported metering identifiers. |
| `price_versions` | Historical prices for a metric, either a default (`customer_id IS NULL`) or a customer override. Each version has an explicit effective timestamp. |
| `rated_usage_groups` | Totals grouped by customer, price version, original usage month, and billing month; stores exact units/gross ticks, booked gross cents, and allocated credit ticks. |
| `usage_ratings` | Links one `(source, event_id)` from `usage_inbox` to exactly one group. Many events can share a group. |
| `monthly_usage` | Gross usage charges in exact ticks per customer and original UTC month, before credit and add-ons. |
| `credit_entries` | Exact signed tick history: positive grants without a group and negative usage debits referencing a group owned by the same customer. `(customer_id, operation_id)` prevents duplicate operations. |
| `addons` | Add-on catalog and monthly price in cents. |
| `addon_subscriptions` | Customer purchases with a monthly price snapshot and the UTC purchase month. One subscription per customer and add-on is supported; cancellation and multiple quantities are future work. |
| `closed_billing_months` | Immutable customer/month closures used to route late consumption forward. |
| `spend_limit_operations` | Immutable operation results; replaying an old identity cannot undo a newer limit. |
| `invoice_closings` | Durable customer/month closing attempts. |
| `invoice_closing_receipts` | Fixed membership of committed pending input at closing, independent of receipt timestamps. |
| `invoices` | One immutable snapshot per customer/month, with unique per-customer number and total cents. Buyer details, ordered lines, exact audit ticks, and issue time are in `snapshot`. |
| `invoiced_usage_groups` | Links issued invoices to groups; a trigger rejects changes to frozen groups. |

Identifiers, balances, prices, and timestamps are supplied explicitly. `customer_billing_state.state_version` defaults to zero and `next_invoice_number` to one for new accounts; callers may supply explicit initial values. Version increments and text validation remain application responsibilities. The inbox remains independent of the catalog: ingestion can preserve unknown customers or metrics for an explicit processing error rather than losing the input.

Foreign keys reject missing catalog records, missing receipts, and credit debits for another customer's group. A group's price must belong to its metric and be either a default or an override for that customer. Group identity cannot change after insertion; totals can increase as the worker processes more events. Rating links require the same customer and metric and an interval wholly within the original UTC month, including intervals ending exactly at the next month's boundary.

Prices, credit entries, and rating links reject row updates and deletes. Price changes append a new version; they do not rewrite past prices. Customer details, account projections, group totals, monthly totals, and the add-on catalog remain mutable. A subscription retains its purchased price when the catalog changes.

## Money and months

All amounts are USD. Credit balances, allocations, and ledger entries use exact integer ticks. Limits, add-on prices, and the booked gross projection use integer cents. The initial price contract supports whole cents per million units: `price_per_million_cents` is `5`, `6`, or `4` for the assignment prices of USD 0.05, 0.06, and 0.04 per million.

One tick is one millionth of a cent, or USD 0.000_000_01. For this price contract:

```text
price_unit_count = 1_000_000
ticks_per_cent = 1_000_000
exact_charge_ticks = units × price_per_million_cents × ticks_per_cent / price_unit_count
```

`accounting.PriceUnitCount` defines the resource units covered by the price; `accounting.TicksPerCent` defines monetary resolution. They currently have equal values and cancel in the formula. [Issue #29](https://github.com/pupitooo/e2b-billing-api/issues/29) proposes storing `price_unit_count` on each historical price version, backfilling existing versions with 1_000_000, and requiring new rates to be exactly representable in whole ticks. That proposal preserves the current tick scale; this implementation still uses a fixed price denominator.

Integer-valued PostgreSQL `numeric` stores exact tick totals and aggregate units beyond the `bigint` range. Negative, fractional, infinite, and NaN values are rejected. The Go rater uses immutable arbitrary-precision integers and checks conversions to signed 64-bit cents. PostgreSQL documents the distinction between [exact numeric and floating-point types](https://www.postgresql.org/docs/18/datatype-numeric.html).

Cyberdyne's first 123_456_789 units produce 617_283_945 ticks, or USD 6.172_839_45. The group can store that exact amount alongside its booked 617 gross cents. Credit is allocated against new exact ticks before rounding. The shared Go helpers round cumulative gross and net groups and derive a balancing credit line; migrations do not perform accounting. `allocated_credit_ticks` cannot exceed `exact_charge_ticks`, even when rounded gross cents are higher.

`usage_month` and `billing_month` are finite first-of-month `date` values. Application code derives them in UTC. Late October usage billed in November retains `usage_month = 2026-10-01` and `billing_month = 2026-11-01`; its gross spend belongs to October. Billing months cannot precede usage months. Timestamps are finite `timestamptz` values. Subscription start months are checked against the purchase timestamp in UTC, independently of the SQL session's time zone.

Default and customer prices can coexist at the same instant. `UNIQUE NULLS NOT DISTINCT (customer_id, metric, effective_from)` prevents two default prices at that instant as well as duplicate customer versions; see [PostgreSQL unique constraints](https://www.postgresql.org/docs/18/ddl-constraints.html#DDL-CONSTRAINTS-UNIQUE-CONSTRAINTS). The rater selects the latest eligible customer override first and otherwise the latest eligible default. It rejects unsupported segments crossing a price boundary; the database does not select the applicable price.

## Initial data and migrations

Run the existing commands:

```sh
make up SERVICE=postgres
make migrate
make migration-status
```

Migration `002_billing_model.sql` creates the model. Migration `003_assignment_seed.sql` loads the assignment's initial data:

| Item | Initial values |
| --- | --- |
| Acme | `acme`, Acme Inc., US, 1 Market St, San Francisco, CA 94105 |
| Cyberdyne | `cyberdyne`, Cyberdyne Systems Corporation, US, 18144 El Camino Real, Sunnyvale, CA 94087 |
| Metric | `cpu_seconds` |
| Default price | 5 cents per million from `2026-10-01T00:00:00Z` |
| Default price | 6 cents per million from `2026-10-15T00:00:00Z` |
| Acme override | 4 cents per million from `2026-10-01T00:00:00Z` |
| Add-on | `concurrency_pack`, 2_000 cents per month |

Both accounts start with zero credit, no spend limit, and state version zero. The example's USD 25 grant, purchase, USD 15 limit, metering events, and invoices are business actions performed by public APIs and the billing simulator, rather than initial catalog data. No financial history or usage is seeded.

Migration `004_exact_credit.sql` converts existing cent credit values to ticks without rewriting migration 002 or its seed. It adds the finite signed-integer domain `billing_signed_ticks` for ledger amounts. See the [conversion and legacy-allocation limitation](accounting-rules.md#migration-and-verification).

The migration runner applies schema, seed, and version records in one transaction under the existing advisory lock. On an existing volume it applies only pending versions. Repeated runs skip the seed, preserving changed account state and customer details. A seed error rolls back the model migration and its version record too.

## Transactional processing and verification

`make test` discovers all `tests/sql/*.sql` files and runs them in UTC and `Asia/Shanghai`. Model tests roll back all fixtures. Seed tests reconstruct the catalog in a private schema inside a rolled-back transaction, so tests also preserve edited application data. The PostgreSQL service's existing read-only `/migrations` mount supplies the seed test scripts.

The worker locks the customer's state row and commits the rating link, group totals, credit debit/balance, original month's gross spend, state version, and inbox `processed_at` together. Constraints represent those records but do not automatically reconcile ledger sums, projection totals, or the calculated price and booked cents. The unique rating link alone does not prove that a complete financial transaction ran exactly once.

Migrations 005–007 persist closure, spend-limit operation results, durable receipt cohorts, invoice snapshots, and group freezes. Closing captures committed pending input, drains it across short transactions, and atomically stores closure, snapshot, freezes, sequence, and state version. Late input outside that cohort routes forward without rewriting issued invoices.

Public command and accounting tests use private schemas. The [billing simulator](../simulator/billing-scenarios.md) verifies the complete assignment through HTTP, including retries, unavailable replies, exact invoices and credit, and monthly-limit reads. Ingestion acknowledges receipt before asynchronous accounting finishes.
