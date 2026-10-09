# E2B Billing API

Billing service for the E2B assignment, built with Go and PostgreSQL. It provides an HTTP API, versioned database migrations, and the `usage_inbox` table.

The project uses the selected [option C architecture](docs/brainstorming/architecture-options.md#why-option-c-was-selected). The [architecture comparison](docs/brainstorming/architecture-options.md) records the design rationale.

## Local setup

### Requirements

Docker with Docker Compose and Make. The PostgreSQL client and Go toolchain run inside containers; no local Go installation is required. Run all `make` commands from the repository root.

### Get the source

If you do not already have a local checkout, clone the repository:

```sh
git clone https://github.com/pupitooo/e2b-billing-api.git
```

### Configuration

Docker Compose reads an optional local `.env` file. The defaults are sufficient for local development. Copy [.env.example](.env.example) to `.env` to change the API port (`E2B_API_PORT`, default `8081`), PostgreSQL port, or development password. Keep the same configuration for subsequent commands.

### Initialize the database

On first setup, start PostgreSQL and apply the schema:

```sh
make up SERVICE=postgres
make migrate
```

The database is now ready for connections. Run migrations again when an update introduces schema changes; see [Database migrations](#database-migrations). An existing database with all migrations applied needs no initialization on restart.

## Running services

Start all implemented services:

```sh
make up
```

`make up` starts the API and PostgreSQL and waits for readiness. It does not apply database migrations. The API skeleton can also run independently of PostgreSQL.

| Command | Purpose |
| --- | --- |
| `make up` | Start all services and wait for readiness. |
| `make up SERVICE=postgres` | Start PostgreSQL and wait for readiness. |
| `make up SERVICE=api` | Build and start the Go API and wait for readiness. |
| `make ps` | Show running and stopped services. |
| `make logs SERVICE=postgres` | Show the last 100 PostgreSQL log lines. |
| `make restart SERVICE=postgres` | Restart PostgreSQL and wait for readiness. |
| `make stop` | Stop services while retaining containers and data. |
| `make down` | Remove containers and the network while retaining database data. |
| `make services` | List available service names: `api` and `postgres`. |
| `make help` | Show all available commands. |

PostgreSQL uses the pinned `postgres:18.6-alpine` image, UTC timestamps, and a named volume. Its port is published on `127.0.0.1`. Database data survives `make stop`, `make restart`, and `make down`.

`E2B_POSTGRES_PASSWORD` initializes the role password when the volume is empty. Changing the environment variable later does not update the password in an existing database.

## Go API

Start the API:

```sh
make up SERVICE=api
```

The API is available at `http://127.0.0.1:8081` by default. `GET /healthz` returns `200` when the HTTP server is available; it does not check the database. The entry point is [cmd/billing-api/main.go](cmd/billing-api/main.go), with routes in [internal/httpapi/handler.go](internal/httpapi/handler.go).

Send a usage request:

```sh
curl -i http://127.0.0.1:8081/usage/batches \
  -H 'Content-Type: application/json' \
  --data '{
    "batch_id": "acme-batch-000001",
    "events": [{
      "schema_version": 1,
      "source": "platform-simulator",
      "event_id": "acme-cpu-000001",
      "customer_id": "acme",
      "sandbox_id": "acme-sandbox-001",
      "metric": "cpu_seconds",
      "period_start": "2026-10-10T12:00:00Z",
      "period_end": "2026-10-10T13:00:00Z",
      "units": 100000000
    }]
  }'
```

The API returns HTTP `202` with `Content-Type: application/json` and this fixed body:

```json
{"status":"accepted"}
```

The handler returns this fixed response for every POST request to `/usage/batches`. It ignores the request body and performs no validation, database writes, deduplication, or accounting. HTTP `202` therefore does not confirm storage of the submitted events.

## Database

### Database connection

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

### Explore the database with psql

`psql` is PostgreSQL's interactive command-line client. It runs inside the container, so no additional database tool needs to be installed locally.

With PostgreSQL running and the [initial migration applied](#initialize-the-database), open a session from your terminal:

```sh
make psql
```

Enter the following commands at the `e2b_billing=>` prompt, rather than in your shell.

List tables:

```text
\dt
```

The current tables are `usage_inbox` (received usage events) and `schema_migrations` (applied migration versions). Inspect the inbox's columns, types, constraints, and indexes:

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

An empty result (`0 rows`) is expected after initial setup. No usage is seeded, and the integrity tests roll back their fixtures.

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

### Database migrations

With PostgreSQL running, apply pending migrations after first setup or an update that adds migrations, then inspect the applied versions:

```sh
make migrate
make migration-status
```

[migrations/migrate.sql](migrations/migrate.sql) is the migration entry point. It acquires a transaction-scoped advisory lock, applies pending migrations, and records each version in `schema_migrations`. Schema changes and version records commit together; a SQL error aborts the transaction. Repeating the command skips applied versions, including when several runners start concurrently.

Migration [001_usage_inbox.sql](migrations/001_usage_inbox.sql) creates the inbox and its partial index for pending, error-free input. Migrations are explicitly invoked, so they also run against an existing Docker volume; restarting the container does not apply them.

To extend the schema, add the next numbered SQL file and a corresponding version check, include, and version record in `migrate.sql`. Once a migration is released, keep it unchanged. The initial migration creates the usage inbox schema.

## Project layout

- `cmd/billing-api/`: application entry point and server setup.
- `internal/`: private application packages, with `*_test.go` package tests next to the code.
- `migrations/`: numbered SQL migrations and the explicit PostgreSQL migration runner.
- `tests/api/`: HTTP integration tests against a running API, enabled with the `integration` build tag.
- `tests/sql/`: database integrity tests executed with `psql`.
- `docs/`: project and interface documentation.

## Testing

`make test` is the primary test command. Start PostgreSQL with `make up SERVICE=postgres` before running all tests or the database suite; Go test commands build and start the API automatically.

| Command | Purpose |
| --- | --- |
| `make test` | Run all test suites. |
| `make go-test` | Run Go package tests without starting external services. |
| `make db-test` | Run only the database integrity suite. |
| `make api-test` | Build and start the API, then run its HTTP happy-path test. |
| `make test SUITE=go` | Run all Go tests, including the HTTP happy path. PostgreSQL is not required for the current HTTP handlers. |
| `make test SUITE=db` | Run only the database integrity suite in both configured time zones. |
| `make test RUN='^TestUsageBatchesHappyPath$'` | Run only the named Go test. Supplying `RUN` selects the Go suite by default. |

`SUITE` accepts `all` (the default), `go`, or `db`. `RUN` uses the standard [Go `-run` regular-expression filter](https://go.dev/src/cmd/go/internal/test/test.go): anchors select an exact test name; a pattern such as `Usage` selects matching names. Explicit `SUITE=all RUN=Usage` runs the full SQL suite and matching Go tests. Unknown suites and `SUITE=db` combined with `RUN` fail before starting test work.

The [handler tests](internal/httpapi/handler_test.go) use `httptest` to check routes, method restrictions, and the current usage response without a running server. With a local Go toolchain, run `go test ./...`; the integration build tag keeps external API tests out of this command. `make go-test` runs the same package tests in a container without starting services.

The [SQL integrity tests](tests/sql/usage_inbox.sql) apply pending migrations, check schema integrity in UTC and `Asia/Shanghai`, and roll back their test data. Output identifies the suite and time zone being tested. Any test failure makes the command fail.

The [API happy-path test](tests/api/usage_batches_test.go) sends a valid Acme measurement to the running service and checks HTTP `202`, JSON content type, and `status: accepted`. It uses HTTP only, without importing the handler. The Go suite runs in a temporary Go container on the Compose network; the API stays running for manual exploration. `-count=1` ensures every invocation actually executes the tests.

With Go 1.27 or later installed locally, the same test can target a running API directly:

```sh
E2B_API_URL=http://127.0.0.1:8081 go test -tags=integration -count=1 -v ./tests/api
```

[CI](.github/workflows/ci.yml) runs `make test` on every push and pull request, using a fresh PostgreSQL volume and the same Compose configuration. The required `Database tests` check now runs both the database and HTTP suites; the branch must also be up to date with `main`. Failed runs include service logs, and each run removes its test containers and volume.

## Usage inbox contract

This section defines the stored measurement contract:

- **Measurement:** consumption interval, metric, and non-negative integer units.
- **Identity and ownership:** event key, schema version, customer, and sandbox.
- **Timestamps and processing state:** explicit application values, UTC conventions, completion, and unresolved errors.
- **Validation and retries:** database constraints and duplicate rejection.

Each row stores a measured increment over the half-open interval `[period_start, period_end)`, rather than a cumulative counter or a monetary charge.[^half-open-interval]

[^half-open-interval]: Including the start and excluding the end gives adjacent intervals an unambiguous boundary: `[10:00, 10:05)` and `[10:05, 10:10)` meet without overlapping, and exactly `10:05` belongs only to the second interval.

| Columns | Meaning |
| --- | --- |
| `source`, `event_id` | Composite primary key identifying one measurement across retries. |
| `schema_version` | Positive contract version, supplied explicitly by the application. |
| `customer_id`, `sandbox_id`, `metric` | Required ownership and metric identifiers. |
| `period_start`, `period_end` | Consumption interval as `timestamptz`; the end must follow the start. |
| `units` | Non-negative `bigint` holding the measured increment. |
| `received_at` | Billing receipt time as `timestamptz`, supplied explicitly by the application. |
| `processed_at` | Completion timestamp; `NULL` for unprocessed input. |
| `processing_error` | Error detail for unresolved input; cannot coexist with a completion timestamp. |

Every required inbox field must be supplied explicitly; the table has no database defaults. PostgreSQL sessions use UTC.

The composite primary key rejects repeated `(source, event_id)` values. The schema enforces required values, positive schema versions, non-negative units, increasing interval endpoints, and consistent processing state. Text content, customer existence, and supported metrics are not validated by the database. The HTTP handler does not write to this table.

## Architecture and HTTP interfaces

![Option C: building blocks and interfaces](docs/diagrams/option-c-components/option-c-components.png)

[Native Mermaid source](docs/diagrams/option-c-components/option-c-components.mmd).

The diagram records the selected architecture. The available HTTP endpoints are:

| Interface | Behavior |
| --- | --- |
| `GET /healthz` | Return HTTP `200` when the HTTP server is available, without checking PostgreSQL. |
| `POST /usage/batches` | Return HTTP `202` and `{"status":"accepted"}` without reading or storing the request body. |

Keep the architecture diagram's Mermaid source and PNG in sync when changing it. Documentation generation tools are local and excluded from the repository.
