# Migrations

Use this page for applying schema changes, upgrade prerequisites, and legacy
conversion checks. [Database](database-operations.md) covers connections and
inspection; [Data model](../architecture/billing-model.md) covers tables and the seed catalog.

## Database migrations

With PostgreSQL running, apply pending migrations after first setup or an update that adds migrations, then inspect the applied versions:

```sh
make migrate
make migration-status
```

[migrations/migrate.sql](../../migrations/migrate.sql) applies only pending versions,
including on existing Docker volumes. Schema changes and version records commit
atomically; concurrent runners are serialized. Restarting a container does not
apply migrations. See the [billing model — Initial data and migrations](../architecture/billing-model.md#initial-data-and-migrations)
for seed values; the migration details below describe conversion and repeat-run behavior.

Before upgrading legacy cent-based credit records, follow the
[legacy conversion checks](#migration-and-verification),
including the check for incompatible allocations that would stop migration 004.

To extend the schema, add the next numbered SQL file and a corresponding version check, include, and version record in `migrate.sql`. Once a migration is released, keep it unchanged.

## Upgrade from the old invoice-closing model

Migration `009` replaces pending-usage capture with closing from processed groups.
It removes `invoice_closings`, `invoice_closing_receipts` and
`invoice_closing_exclusions`, retaining usage, financial history and issued invoices.
Stop the API and worker before migrating so the previous application cannot access
retired tables; then start both from the updated source:

```sh
make stop SERVICE=api
make stop SERVICE=worker
make migrate
make up
```

An unfinished old closing has no reserved cutoff after this migration. Its already
committed accounting remains, and its next successful close uses the processed
groups available at the new account-lock cutoff. Pending usage may be absent from
that invoice and is accounted later by the worker. A failed closing leaves no
partial snapshot or consumed number; only a successfully committed invoice fixes
its contents. Different billing months retain separate group rounding boundaries.

## Shared command-history foundation

The existing foundation migrations were revised during local review. Migration
versions 006 and 011 are reserved no-ops, preserving the remaining version numbers.
A database built from the previous definitions must have its schema recreated
before applying the revised foundation. Clearing `schema_migrations` alone does
not remove old tables or columns. Ordinary `make migrate` skips applied versions;
this change provides no upgrade or historical-key backfill for an old schema.

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

## Transactional processing and verification

`make test` discovers all `tests/sql/*.sql` files and runs them in UTC and `Asia/Shanghai`. The non-UTC session checks that any values derived from the database session's default time zone remain correct when that zone differs from the application logic, which always uses UTC. This exposes accidental dependence on the session time zone, especially at month boundaries. Model tests roll back all fixtures. Seed tests reconstruct the catalog in a private schema inside a rolled-back transaction, so tests also preserve edited application data. The PostgreSQL service's existing read-only `/migrations` mount supplies the seed test scripts.

The worker locks the customer's state row and commits the rating link, group totals, credit debit/balance, original month's gross spend, state version, and inbox `processed_at` together. Constraints represent those records but do not automatically reconcile ledger sums, projection totals, or the calculated price and booked cents. The unique rating link alone does not prove that a complete financial transaction ran exactly once.

Migrations 005 and 007 persist accounting, closed months, invoices and group freezes; version 006 is a reserved no-op. Migration 008 introduced error exclusions for the former pending-usage capture flow; migration 009 retires that flow and all three supporting tables. Closing now publishes only already processed groups in one atomic transaction. Usage processed later routes forward without rewriting issued invoices.

Migration `010_api_idempotency.sql` creates shared durable request-key history for prices, add-ons, credit grants, and spend-limit changes. The caller supplies only `idempotency_key`; internal resource and ledger IDs are generated independently on the server. Versions 006 and 011 are reserved no-ops. The revised foundation requires a clean database schema and supplies no upgrade or backfill for the previous split storage.

Migration `012_accounting_id_sequences.sql` adds owned, noncycling `bigint`
sequences for new `grp_<number>` group IDs and `crd_<number>` credit entry IDs.
Each namespace starts at one unless existing canonical decimal IDs reserve higher
values. Existing rows and all references remain unchanged. Groups are located by
their customer, price version, usage month and billing month before allocation;
credit entries retain their separate operation identity for retries. Sequence
numbers are not rolled back, so failed financial transactions can leave gaps.
See the [accounting identity rules](../architecture/accounting-rules.md#accounting-resource-identities).

Public command and accounting tests use private schemas. The [billing simulator](../simulator/billing-scenarios.md) verifies the complete assignment through HTTP, including retries, unavailable replies, exact invoices and credit, and monthly-limit reads. Ingestion acknowledges receipt before asynchronous accounting finishes.
