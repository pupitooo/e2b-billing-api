# E2B Billing API

Billing service for the E2B assignment, using Go and the option C architecture: HTTP ingestion, a durable PostgreSQL inbox, and asynchronous accounting workers.

The current implementation provides PostgreSQL, versioned schema migrations, and the `usage_inbox` table. The Go API, platform simulator, accounting worker, and financial tables are planned next; there are no running HTTP endpoints yet.

## Quick start

Requirements: Docker with Docker Compose and Make. The PostgreSQL client runs inside the container.

```sh
git clone https://github.com/pupitooo/e2b-billing-api.git
cd e2b-billing-api
make up
make migrate
make migration-status
make test
```

`make up` starts the services and waits for readiness. `make migrate` applies pending migrations to a fresh or existing database. `make test` checks inbox integrity and rolls back its fixtures.

## Local configuration and service commands

Docker Compose reads an optional local `.env` file. Copy `.env.example` to `.env` to change the host port or development password. Keep the same configuration for subsequent commands.

| Command | Purpose |
| --- | --- |
| `make up` | Start all services and wait for readiness. |
| `make up SERVICE=postgres` | Start PostgreSQL and wait for readiness. |
| `make ps` | Show running and stopped services. |
| `make logs SERVICE=postgres` | Show the last 100 PostgreSQL log lines. |
| `make restart SERVICE=postgres` | Restart PostgreSQL and wait for readiness. |
| `make stop` | Stop services while retaining containers and data. |
| `make down` | Remove containers and the network while retaining database data. |
| `make services` | List available service names; currently `postgres`. |
| `make help` | Show all available commands. |

PostgreSQL uses the pinned `postgres:18.6-alpine` image, UTC timestamps, and a named volume. Its port is published on `127.0.0.1`. Database data survives `make stop`, `make restart`, and `make down`.

`E2B_POSTGRES_PASSWORD` initializes the role password when the volume is empty. Changing the environment variable later does not update the password in an existing database.

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

Open an interactive SQL session:

```sh
make psql
```

Inside the session, run `\d usage_inbox` to inspect the table, `TABLE schema_migrations;` to inspect applied migrations, and `\q` to exit.

## Database migrations

```sh
make migrate
make migration-status
```

[migrations/migrate.sql](migrations/migrate.sql) is the migration entry point. It acquires a transaction-scoped advisory lock, applies pending migrations, and records each version in `schema_migrations`. Schema changes and version records commit together; a SQL error aborts the transaction. Repeating the command skips applied versions, including when several runners start concurrently.

Migration [001_usage_inbox.sql](migrations/001_usage_inbox.sql) creates the inbox and its partial index for pending, error-free input. Migrations are explicitly invoked, so they also run against an existing Docker volume; restarting the container does not apply them.

To extend the schema, add the next numbered SQL file and a corresponding version check, include, and version record in `migrate.sql`. Keep applied migrations unchanged. The initial migration creates the receipt schema; assignment customers, prices, credit, and invoices will be introduced with their own tables and seed data.

## Usage inbox contract

Each row stores a measured increment over the half-open interval `[period_start, period_end)`, rather than a cumulative counter or a monetary charge.

| Columns | Meaning |
| --- | --- |
| `source`, `event_id` | Composite primary key identifying one measurement across retries. |
| `schema_version` | Positive contract version; defaults to `1`. |
| `customer_id`, `sandbox_id`, `metric` | Ownership and metric identifiers; blank values are rejected. |
| `period_start`, `period_end` | Consumption interval as `timestamptz`; the end must follow the start. |
| `units` | Non-negative `bigint` holding the measured increment. |
| `received_at` | Database receipt time, assigned by default. |
| `processed_at` | Completion timestamp; `NULL` for unprocessed input. |
| `processing_error` | Error detail for unresolved input; cannot coexist with a completion timestamp. |

The database rejects a repeated `(source, event_id)` and preserves the original row. The future ingestion API must compare the original content: an identical retry is accepted without another insert, while changed content for the same identity is a conflict. Customer existence and supported metric validation will be added with the corresponding application logic.

## Architecture and planned HTTP interfaces

![Option C: building blocks and interfaces](docs/diagrams/option-c-components/option-c-components.png)

[Native Mermaid source](docs/diagrams/option-c-components/option-c-components.mmd).

The highlighted inbox is implemented in this change. The other blocks describe the intended architecture. API and worker modules may initially share one Go process. The two storage blocks represent tables in one PostgreSQL database.

| Planned interface | Purpose |
| --- | --- |
| `POST /usage/batches` | Validate and store usage; return `202` after the inbox transaction commits. |
| `GET /customers/{id}/spend-status` | Return the current UTC month, processed gross spend, limit status, and processing lag. |

An accepted event may still await accounting. The worker will claim pending rows through SQL, apply prices and credit under the customer lock, and commit the financial effect with the inbox completion marker in one transaction. Monthly closing must wait for the customer's fixed boundary of accepted input before issuing an immutable invoice. The platform polls spend status independently of sending new usage.

## Verification and diagram rendering

```sh
make test
```

The SQL tests verify receipt defaults, duplicate identity rejection, preservation of original units, independent source namespaces, large integer totals, interval and identifier constraints, and valid processing state transitions. They run in a transaction and roll back their fixtures.

To regenerate the PNG, install Node.js and Google Chrome, then run:

```sh
cd tools/diagrams
npm ci
npm run render
```

The renderer writes PNGs next to their native Mermaid sources. Diagram tooling is separate from the database startup and migration commands.
