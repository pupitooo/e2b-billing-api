# Billing model and assignment seed data

The schema prepares PostgreSQL for the Go accounting worker. It stores the initial catalog and accounting records; rating, credit allocation, spend-status endpoints, and invoices follow in later changes.

## Tables and relationships

| Table | Purpose and relationships |
| --- | --- |
| `customers` | Customer identity, name, country, and current billing address. |
| `customer_billing_state` | One account row per customer with credit balance in cents, optional spend limit in cents, and state version. Future financial writers lock this row. |
| `metrics` | Supported metering identifiers. |
| `price_versions` | Historical prices for a metric, either a default (`customer_id IS NULL`) or a customer override. Each version has an explicit effective timestamp. |
| `rated_usage_groups` | Totals grouped by customer, price version, original usage month, and billing month; stores exact units/ticks, booked cents, and allocated credit cents. |
| `usage_ratings` | Links one `(source, event_id)` from `usage_inbox` to exactly one group. Many events can share a group. |
| `monthly_usage` | Gross usage charges in exact ticks per customer and original UTC month, before credit and add-ons. |
| `credit_entries` | Credit history: positive grants without a group and negative usage debits referencing a group owned by the same customer. `(customer_id, operation_id)` prevents duplicate operations. |
| `addons` | Add-on catalog and monthly price in cents. |
| `addon_subscriptions` | Customer purchases with a monthly price snapshot and the UTC purchase month. One subscription per customer and add-on is supported; cancellation and multiple quantities are future work. |

Identifiers, balances, versions, and timestamps are supplied explicitly. New model tables have no database defaults. Text validation remains an application responsibility. The inbox remains independent of the catalog: ingestion can preserve unknown customers or metrics for an explicit processing error rather than losing the input.

Foreign keys reject missing catalog records, missing receipts, and credit debits for another customer's group. A group's price must belong to its metric and be either a default or an override for that customer. Group identity cannot change after insertion; totals can increase as the worker processes more events. Rating links require the same customer and metric and an interval wholly within the original UTC month, including intervals ending exactly at the next month's boundary.

Prices, credit entries, and rating links reject row updates and deletes. Price changes append a new version; they do not rewrite past prices. Customer details, account projections, group totals, monthly totals, and the add-on catalog remain mutable. A subscription retains its purchased price when the catalog changes.

## Money and months

All amounts are USD. Balances, limits, add-on prices, booked usage, and credit entries use integer cents. The initial price contract supports whole cents per million units: `price_per_million_cents` is `5`, `6`, or `4` for the assignment prices of USD 0.05, 0.06, and 0.04 per million. Finer price precision would require a new explicit contract and migration.

One tick is one millionth of a cent, or USD 0.00000001. For this price contract:

```text
exact_charge_ticks = units × price_per_million_cents
1 cent = 1,000,000 ticks
```

Integer-valued PostgreSQL `numeric` stores exact tick totals and aggregate units beyond the `bigint` range. Negative, fractional, infinite, and NaN values are rejected. The future Go rater must use checked integer arithmetic or arbitrary-precision integers. PostgreSQL documents the distinction between [exact numeric and floating-point types](https://www.postgresql.org/docs/18/datatype-numeric.html).

Cyberdyne's first 123,456,789 units produce 617,283,945 ticks, or USD 6.17283945. The group can store that exact amount alongside its booked 617 cents. Keeping both supports the planned half-up rounding of cumulative groups and charging only each new cent increment, avoiding a different result when usage is split among minute or sandbox events. This migration does not calculate those amounts or perform rounding.

`usage_month` and `billing_month` are finite first-of-month `date` values. Application code derives them in UTC. Late October usage billed in November retains `usage_month = 2026-10-01` and `billing_month = 2026-11-01`; its gross spend belongs to October. Billing months cannot precede usage months. Timestamps are finite `timestamptz` values. Subscription start months are checked against the purchase timestamp in UTC, independently of the SQL session's time zone.

Default and customer prices can coexist at the same instant. `UNIQUE NULLS NOT DISTINCT (customer_id, metric, effective_from)` prevents two default prices at that instant as well as duplicate customer versions; see [PostgreSQL unique constraints](https://www.postgresql.org/docs/18/ddl-constraints.html#DDL-CONSTRAINTS-UNIQUE-CONSTRAINTS). The future rater selects the latest eligible customer override first and otherwise the latest eligible default. It must reject unsupported segments crossing a price boundary; the database does not select the applicable price.

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
| Add-on | `concurrency_pack`, 2,000 cents per month |

Both accounts start with zero credit, no spend limit, and state version zero. The example's USD 25 grant, purchase, USD 15 limit, metering events, and invoices are business actions for subsequent API/simulator work, not initial catalog data. No financial history or usage is seeded.

The migration runner applies schema, seed, and version records in one transaction under the existing advisory lock. On an existing volume it applies only pending versions. Repeated runs skip the seed, preserving changed account state and customer details. A seed error rolls back the model migration and its version record too.

## Verification and remaining processing work

`make test` discovers all `tests/sql/*.sql` files and runs them in UTC and `Asia/Shanghai`. Model tests roll back all fixtures. Seed tests reconstruct the catalog in a private schema inside a rolled-back transaction, so tests also preserve edited application data. The PostgreSQL service's existing read-only `/migrations` mount supplies the seed test scripts.

The future worker must lock the customer's state row and commit the rating link, group totals, credit debit/balance, original month's gross spend, state version, and inbox `processed_at` together. Constraints represent those records but do not automatically reconcile ledger sums, projection totals, or the calculated price and booked cents. The unique rating link alone does not prove that a complete financial transaction ran exactly once.

Invoice tables, immutable invoice snapshots, closing/frozen groups, and routing late usage around a completed invoice are subsequent work. The two stored months preserve the information that implementation will need. This change adds no HTTP endpoints and does not perform accounting when an event is accepted.
