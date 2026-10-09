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

Docker Compose reads an optional local `.env` file. The defaults are sufficient for local development. Copy [.env.example](.env.example) to `.env` to change the API port (`E2B_API_PORT`, default `8081`), documentation port (`E2B_DOCS_PORT`, default `8082`), PostgreSQL port, or development password. Keep the same configuration for subsequent commands.

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

`make up` starts PostgreSQL, the API, and Scalar documentation and waits for readiness. It does not apply database migrations. The API verifies its database connection on startup; ingestion returns `503` until the inbox migration has been applied.

`make up`, `make docs`, `make restart`, and `make ps` print the actual browser addresses of running HTTP services. Use `make links` to show them again.

| Command | Purpose |
| --- | --- |
| `make up` | Start all services and wait for readiness. |
| `make up SERVICE=postgres` | Start PostgreSQL and wait for readiness. |
| `make up SERVICE=api` | Build and start the Go API and wait for readiness. |
| `make docs` | Start Scalar documentation and the API for browser requests. |
| `make ps` | Show running and stopped services. |
| `make links` | Show browser links for running HTTP services. |
| `make logs SERVICE=postgres` | Show the last 100 PostgreSQL log lines. |
| `make restart SERVICE=postgres` | Restart PostgreSQL and wait for readiness. |
| `make stop` | Stop services while retaining containers and data. |
| `make down` | Remove containers and the network while retaining database data. |
| `make services` | List available service names: `api`, `docs`, and `postgres`. |
| `make help` | Show all available commands. |

PostgreSQL uses the pinned `postgres:18.6-alpine` image, UTC timestamps, and a named volume. Its port is published on `127.0.0.1`. Database data survives `make stop`, `make restart`, and `make down`.

`E2B_POSTGRES_PASSWORD` initializes the role password when the volume is empty. Changing the environment variable later does not update the password in an existing database.

## Go API

Start the API:

```sh
make up SERVICE=api
```

The API is available at `http://127.0.0.1:8081` by default. `GET /healthz` returns `200` when the HTTP server is available; it does not check the database. The entry point is [cmd/billing-api/main.go](cmd/billing-api/main.go), with routes in [internal/httpapi/handler.go](internal/httpapi/handler.go).

Compose supplies the PostgreSQL connection through standard `PGHOST`, `PGPORT`,
`PGDATABASE`, `PGUSER`, `PGPASSWORD`, and `PGSSLMODE` variables. A process
started outside Compose can supply these variables or `DATABASE_URL`; sessions
always use UTC. Starting the API or documentation also starts PostgreSQL.

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

For a valid batch, the API returns HTTP `202` with `Content-Type: application/json` and this fixed body:

```json
{"status":"accepted"}
```

The handler validates the whole batch and returns `202` only after a synchronous
PostgreSQL commit. New rows receive one explicit UTC receipt time and pending
accounting state. The whole batch commits or rolls back; accounting remains
asynchronous and is subsequent work.

Measurements use `(source, event_id)` as identity. Identical retries succeed,
including duplicates within a batch, regrouped batches, and different timestamp
offsets for the same instant. Comparison includes schema version, customer,
sandbox, metric, both interval endpoints, and units. Retries preserve original
`received_at`, `processed_at`, and `processing_error`. Changed content returns
`409` and rolls back all new rows in that request. The optional `batch_id` is
accepted producer metadata; it is not stored or used for deduplication.

Requests require uncompressed UTF-8 `application/json`, one JSON document,
case-sensitive field names, and no unknown or duplicate members. The body limit
is 1 MiB (1048576 bytes), with 1–1000 events and at most 256 UTF-8 bytes per
identifier or optional `batch_id`. Every required event field must be explicit
and non-null; zero units are valid. Versions and units use int32/int64 integer
tokens without decimal or exponent notation. Consumption times require valid
RFC 3339 calendar values, an explicit offset, and at most six fractional digits.
Identifiers are preserved and time instants normalize to UTC.

| Status | Behavior |
| --- | --- |
| `202` | Whole batch durably committed; identical existing measurements preserved. |
| `400` | Invalid or ambiguous JSON, incorrect types, or numeric decoding outside int32/int64 ranges. |
| `413` | The body exceeds 1048576 bytes. |
| `415` | Unsupported content type, charset, or compression. |
| `422` | Missing/null fields, invalid measurement values, or an event-count limit violation. |
| `409` | Different measurement content for one event identity, stored or repeated in the request. |
| `503` | Inbox unavailable, transaction canceled/timed out, or commit failed; outcome may be unknown. |

Errors use `{"error":{"code":"invalid_batch","message":"...","field":"events[0].units"}}`,
with `field` included when a validation location is available. A `409` uses code
`event_conflict` and identifies `source` and `event_id`; retain that input for
investigation. Correct invalid requests before retrying.

Database work has a 10-second deadline and honors request cancellation. A `503`
uses code `inbox_unavailable` with `Retry-After: 1`, without exposing SQL details.
Retain events and retry the same identities and content after backoff. A `503`
or a lost response may occur after commit; idempotence makes unchanged retries
safe. See the [OpenAPI specification](docs/api/openapi.yaml) for the full contract.

## API documentation

Run `make docs` and open the printed documentation address (by default
[http://127.0.0.1:8082](http://127.0.0.1:8082)). The single Scalar service uses
the `modern` layout and an embedded **Test Request** client. It serves the
[OpenAPI 3.1.2 specification](docs/api/openapi.yaml) and bundled assets locally,
with browser requests forwarded through the same-origin `/api` proxy in
[docs/api/Caddyfile](docs/api/Caddyfile). Refresh the page after editing the
mounted specification. Every interface change must update this specification;
implemented ingestion and asynchronous accounting semantics are described there.

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
- `tests/inbox/`: PostgreSQL repository tests in isolated temporary schemas, enabled with the `integration` build tag.
- `tests/sql/`: database integrity tests executed with `psql`.
- `docs/`: project and interface documentation.

## Testing

`make test` is the primary test command. Start PostgreSQL with `make up SERVICE=postgres` before running all tests or the database suite; Go test commands build and start the API automatically.

| Command | Purpose |
| --- | --- |
| `make test` | Run all test suites. |
| `make go-test` | Run Go package tests without starting external services. |
| `make db-test` | Run only the database integrity suite. |
| `make api-test` | Start PostgreSQL, migrate, and run HTTP/database acceptance tests against the API. |
| `make inbox-test` | Start PostgreSQL and run repository tests in private schemas. |
| `make test SUITE=go` | Run Go unit, PostgreSQL repository, and HTTP acceptance tests; services start automatically. |
| `make test SUITE=db` | Run only the database integrity suite in both configured time zones. |
| `make test RUN='^TestUsageBatchesHappyPath$'` | Run only the named Go test. Supplying `RUN` selects the Go suite by default. |

`SUITE` accepts `all` (the default), `go`, or `db`. `RUN` uses the standard [Go `-run` regular-expression filter](https://go.dev/src/cmd/go/internal/test/test.go): anchors select an exact test name; a pattern such as `Usage` selects matching names. Explicit `SUITE=all RUN=Usage` runs the full SQL suite and matching Go tests. Unknown suites and `SUITE=db` combined with `RUN` fail before starting test work.

The [handler tests](internal/httpapi/handler_test.go) use `httptest` to check routes, method restrictions, and the current usage response without a running server. With a local Go toolchain, run `go test ./...`; the integration build tag keeps external API tests out of this command. `make go-test` runs the same package tests in a container without starting services.

The [SQL integrity tests](tests/sql/usage_inbox.sql) apply pending migrations, check schema integrity in UTC and `Asia/Shanghai`, and roll back their test data. Output identifies the suite and time zone being tested. Any test failure makes the command fail.

The original [API happy-path request](tests/api/usage_batches_test.go) and response
expectations remain unchanged. The [acceptance tests](tests/api/acceptance_test.go)
send HTTP requests to the running service and inspect committed rows through
a separate PostgreSQL connection. They cover durable receipt, preserved retry
metadata, atomic conflicts, invalid later events, and concurrent HTTP retries.
They remove only their owned rows, retaining any pre-existing fixed happy-path
fixture. The [repository tests](tests/inbox/idempotence_test.go) additionally
exercise deterministic lock waits, competing commits/rollbacks, canceled
transactions, and each conflicting content field. Each test drops its private
schema. The API stays running for exploration; `-count=1` executes every test.

With Go 1.27 or later installed locally, the same test can target a running API directly:

```sh
E2B_API_URL=http://127.0.0.1:8081 \
E2B_TEST_DATABASE_URL='postgres://e2b:e2b_local_dev@127.0.0.1:5432/e2b_billing?sslmode=disable' \
go test -tags=integration -count=1 -v ./tests/api ./tests/inbox
```

[CI](.github/workflows/ci.yml) runs `make test` on every push and pull request, using a fresh PostgreSQL volume and the same Compose configuration. The required `All tests` check runs SQL integrity tests, Go package tests, PostgreSQL repository tests, and HTTP integration tests; the branch must also be up to date with `main`. Failed runs include service logs, and each run removes its test containers and volume.

## Usage inbox contract

This section defines the stored measurement contract:

- **Measurement:** consumption interval, metric, and non-negative integer units.
- **Identity and ownership:** event key, schema version, customer, and sandbox.
- **Timestamps and processing state:** explicit application values, UTC conventions, completion, and unresolved errors.
- **Validation and retries:** application validation, content comparison, and atomic storage.

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

The composite primary key prevents repeated `(source, event_id)` rows. The
[PostgreSQL repository](internal/inbox/postgres.go) inserts with conflict
detection, then compares stored content in a fresh READ COMMITTED statement.
All writers sort keys before acquiring locks, preventing deadlocks between
overlapping batches in opposite input order. Identical measurements are read
without updating receipt or accounting state; different content aborts the
transaction. The database enforces required values, positive schema versions,
non-negative units, increasing endpoints, and consistent processing state.
Text content, customer existence, and supported metrics are not validated by
the database. The application validates text and finite timestamp precision.

The [Go event model](internal/usage/event.go) provides standalone application
validation of measurement values: identifiers must contain a non-whitespace
character, use valid UTF-8, and contain no NUL characters; schema versions must
be positive and units non-negative. Interval endpoints must be supplied, have
UTC years from 1 through 9999, use at most microsecond precision, and increase
when compared as instants. Finer timestamp precision is rejected to avoid losing
measurement content when it is later stored in PostgreSQL. Customer existence
and supported version or metric registries are separate concerns.

The [event tests](internal/usage/event_test.go) cover required values, integer
boundaries, Unicode whitespace, timestamp precision, and intervals across time
zones and UTC month boundaries. Run them with `make go-test RUN=EventValidate`.
The HTTP parser now invokes this validator after checking JSON field presence,
including the distinction between omitted units and valid zero units. It reports
the first invalid value with its event index and field name; no partial batch is
acknowledged. Only successful repository commit produces an acknowledgement.

## Architecture and HTTP interfaces

![Option C: building blocks and interfaces](docs/diagrams/option-c-components/option-c-components.png)

[Native Mermaid source](docs/diagrams/option-c-components/option-c-components.mmd).

The diagram records the selected architecture. The available HTTP endpoints are:

| Interface | Behavior |
| --- | --- |
| `GET /healthz` | Return HTTP `200` when the HTTP server is available, without checking PostgreSQL. |
| `POST /usage/batches` | Validate and atomically commit measurements, return durable `202`, report changed content as `409`, and permit unchanged retries after `503`. |

Keep the architecture diagram's Mermaid source and PNG in sync when changing it. Documentation generation tools are local and excluded from the repository.
