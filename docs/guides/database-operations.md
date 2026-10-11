# Database

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

Each [Compose project](local-development.md#compose-projects-and-ports) has its own
database volume, even though the database name and credentials can be identical.
Keep the intended `COMPOSE_PROJECT_NAME` and port exports for migrations and
`make psql`. Open Adminer through that project's portal link from `make links`;
its internal `postgres` hostname connects to the database in the same project.
Changing a host port alone does not select another database.

## Explore the database with psql

`psql` is PostgreSQL's interactive command-line client. It runs inside the container, so no additional database tool needs to be installed locally.

With PostgreSQL running and the [initial migration applied](local-development.md#initialize-the-database), open a session from your terminal:

```sh
make psql
```

Enter the following commands at the `e2b_billing=>` prompt, rather than in your shell.

List tables:

```text
\dt
```

The tables include `usage_inbox` (received events), `schema_migrations` (migration versions), and the [billing model tables](../architecture/billing-model.md#tables-and-relationships). Inspect the inbox's columns, types, constraints, and indexes:

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

## Inspect API operation keys

```sql
SELECT operation_scope, idempotency_key, request_payload, response_payload, created_at
FROM api_idempotency_operations
ORDER BY created_at, operation_scope, idempotency_key;
```

See the [command identities](../architecture/billing-model.md#durable-api-operation-keys)
for storage and retention rules.

## Inspect accounting IDs

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

For schema changes and upgrade procedures, follow [migrations](migrations.md).
