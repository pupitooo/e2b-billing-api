# Database connections, inspection, and migrations

## Database connection

| Setting | Default |
| --- | --- |
| Host from a local application | `127.0.0.1` |
| Host from a service in the Compose network | `postgres` |
| Port | `5432` |
| Database | `e2b_billing` |
| User | `e2b` |
| Development password | `e2b_local_dev` |
| Server time zone | `UTC` |

Connection string for a local application, using the default development settings:

```text
postgres://e2b:e2b_local_dev@127.0.0.1:5432/e2b_billing?sslmode=disable
```

Use `postgres` as the host from another Compose service. If the host port changes, update the local application connection string accordingly.

## Explore the database with psql

`psql` is PostgreSQL's interactive command-line client. It runs inside the container, so no additional database tool needs to be installed locally.

With PostgreSQL running and the [initial migration applied](../guides/local-development.md#initialize-the-database), open a session from your terminal:

```sh
make psql
```

Enter the following commands at the `e2b_billing=>` prompt, rather than in your shell.

List tables:

```text
\dt
```

The tables include `usage_inbox` (received events), `schema_migrations` (migration versions), and the [billing model tables](../../docs/architecture/billing-model.md#tables-and-relationships). Inspect the inbox's columns, types, constraints, and indexes:

```text
\d usage_inbox
```

Show up to 50 inbox rows, with the most recently received events first:

```sql
SELECT *
FROM usage_inbox
ORDER BY received_at DESC, source, event_id
LIMIT 50;
```

An empty inbox (`0 rows`) is expected after initial setup. Migrations seed the customer, price, metric, and add-on catalog; no usage or example business actions are seeded, and integrity tests roll back their fixtures. Inspect initial account state:

```sql
TABLE customers;
TABLE customer_billing_state;
SELECT price_version_id, customer_id, metric,
       price_per_million_cents / 100.0 AS usd_per_million, effective_from
FROM price_versions
ORDER BY metric, customer_id NULLS FIRST, effective_from;
TABLE addons;
```

You can enter any SQL query in this session. End each SQL statement with a semicolon. For example, count stored events:

```sql
SELECT count(*) AS event_count FROM usage_inbox;
```

List events waiting for processing, excluding unresolved errors:

```sql
SELECT source, event_id, customer_id, metric, units, received_at
FROM usage_inbox
WHERE processed_at IS NULL AND processing_error IS NULL
ORDER BY received_at, source, event_id
LIMIT 50;
```

Inspect migration records:

```sql
TABLE schema_migrations;
```

Useful `psql` commands (these do not need a semicolon):

| Command | Purpose |
| --- | --- |
| `\x auto` | Automatically use a vertical layout for wide query results. |
| `\?` | Show help for `psql` commands. |
| `\h SELECT` | Show SQL syntax help for `SELECT`. |
| `\q` | Exit the session and return to your terminal. |

If output opens in a pager, press `q` to return to the SQL prompt. If you are partway through a query, press Ctrl+C to clear it and start again.

## Database migrations

With PostgreSQL running, apply pending migrations after first setup or an update that adds migrations, then inspect the applied versions:

```sh
make migrate
make migration-status
```

[migrations/migrate.sql](../../migrations/migrate.sql) applies only pending versions,
including on existing Docker volumes. Schema changes and version records commit
atomically; concurrent runners are serialized. Restarting a container does not
apply migrations. See the [billing model — Initial data and migrations](../../docs/architecture/billing-model.md#initial-data-and-migrations)
for migration contents, seed values, and repeat-run behavior.

Before upgrading legacy cent-based credit records, follow the
[financial rules — Migration and verification](../../docs/architecture/accounting-rules.md#migration-and-verification),
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

## Durable API operation keys

Migration `010_api_idempotency.sql` adds append-only `api_idempotency_operations`.
Apply it with `make migrate` before starting the updated API. Existing prices,
subscriptions, and invoices retain their IDs. Price and add-on creation bodies replace caller resource IDs with required JSON
`idempotency_key`; old resource commands do not acquire inferred keys. Migration
`011_spend_limit_idempotency_key.sql`
renames the spend-limit history column, preserving its rows, primary key, and
append-only trigger. Credit and limit callers send their previous `operation_id`
value as `idempotency_key`; the credit ledger retains internal grant/debit operation
IDs. Run `make migrate` before rebuilding API and worker services.

```sql
SELECT operation_scope, idempotency_key, request_payload, response_payload, created_at
FROM api_idempotency_operations
ORDER BY created_at, operation_scope, idempotency_key;
```

Successful keys and results are retained indefinitely. They commit in the same
transaction as their financial resource, so retry needs no in-memory API state.

## Sequential accounting IDs

Migration `012_accounting_id_sequences.sql` adds persistent, noncycling sequences
for new `grp_<number>` and `crd_<number>` IDs. Apply `make migrate` before starting
the updated API and worker. Existing group IDs, ledger entries, references and
issued invoices retain their original values. Each sequence starts at one unless
earlier canonical decimal IDs reserve numbers. Allocation may leave gaps after
rollback; do not reset or cycle these sequences.

Inspect their current state without consuming a number:

```sql
SELECT 'rated_usage_group_id_seq' AS sequence_name, last_value, is_called
FROM rated_usage_group_id_seq
UNION ALL
SELECT 'credit_entry_id_seq', last_value, is_called
FROM credit_entry_id_seq;
```

`is_called = false` means the first value has not been allocated. See the
[accounting identity rules](../architecture/accounting-rules.md#accounting-resource-identities)
for retry, concurrency and sequence exhaustion behavior.
