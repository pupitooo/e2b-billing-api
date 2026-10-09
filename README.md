# E2B Billing API

Billing service for the E2B assignment, using Go and the selected [option C architecture](docs/brainstorming/architecture-options.md#why-option-c-was-selected): HTTP ingestion, a durable PostgreSQL inbox, and asynchronous accounting workers. The [architecture comparison](docs/brainstorming/architecture-options.md) describes options A–D, their diagrams, and TODOs for further design and higher load.

The current implementation provides PostgreSQL, versioned schema migrations, the `usage_inbox` table, and a Go HTTP API skeleton. `POST /usage/batches` returns a fixed response for interface exploration; it does not validate or store usage yet. The platform simulator, accounting worker, and financial tables remain planned.

The [platform and billing contract](#platform-and-billing-contract) records proposed delivery responsibilities, acknowledgement rules, and agreements still to be made.

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

`make up` starts the API and PostgreSQL and waits for readiness. It does not apply database migrations. The API skeleton can also run independently of PostgreSQL. The platform simulator and accounting worker will use the same Compose file and service commands as they are added.

| Command | Purpose |
| --- | --- |
| `make up` | Start all services and wait for readiness. |
| `make up SERVICE=postgres` | Start PostgreSQL and wait for readiness. |
| `make up SERVICE=api` | Build and start the Go API skeleton and wait for readiness. |
| `make ps` | Show running and stopped services. |
| `make logs SERVICE=postgres` | Show the last 100 PostgreSQL log lines. |
| `make restart SERVICE=postgres` | Restart PostgreSQL and wait for readiness. |
| `make stop` | Stop services while retaining containers and data. |
| `make down` | Remove containers and the network while retaining database data. |
| `make services` | List available service names: `api` and `postgres`. |
| `make help` | Show all available commands. |

PostgreSQL uses the pinned `postgres:18.6-alpine` image, UTC timestamps, and a named volume. Its port is published on `127.0.0.1`. Database data survives `make stop`, `make restart`, and `make down`.

`E2B_POSTGRES_PASSWORD` initializes the role password when the volume is empty. Changing the environment variable later does not update the password in an existing database.

## Go API skeleton

Start the API:

```sh
make up SERVICE=api
```

The API is available at `http://127.0.0.1:8081` by default. `GET /healthz` returns `200` when the HTTP server is available; it does not check the database. The entry point is [cmd/billing-api/main.go](cmd/billing-api/main.go), with routes in [internal/httpapi/handler.go](internal/httpapi/handler.go).

Try the proposed usage request:

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

The skeleton returns HTTP `202` with `Content-Type: application/json` and this fixed body:

```json
{"status":"accepted"}
```

**This response is a stub, not a durable receipt.** The handler ignores the request body and performs no validation, database writes, deduplication, or accounting. It exists to explore the interface before implementing ingestion in a separate PR. Once ingestion is implemented, a successful response will confirm the committed batch; the delivery guarantees below describe that intended behavior.

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

An empty result (`0 rows`) is expected after initial setup. No usage is seeded yet, and the integrity tests roll back their fixtures.

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

To extend the schema, add the next numbered SQL file and a corresponding version check, include, and version record in `migrate.sql`. Once a migration is released, keep it unchanged. The initial migration creates the receipt schema; assignment customers, prices, credit, and invoices will be introduced with their own tables and seed data.

## Project layout

- `cmd/billing-api/`: application entry point and server setup.
- `internal/`: private application packages, with `*_test.go` package tests next to the code.
- `migrations/`: numbered SQL migrations and the explicit PostgreSQL migration runner.
- `tests/api/`: HTTP integration tests against a running API, enabled with the `integration` build tag.
- `tests/sql/`: database integrity tests executed with `psql`.
- `docs/`: project and interface documentation.

## Testing

With PostgreSQL running:

| Command | Purpose |
| --- | --- |
| `make test` | Run all test suites. |
| `make go-test` | Run Go package tests without starting external services. |
| `make db-test` | Run only the database integrity suite. |
| `make api-test` | Build and start the API, then run its HTTP happy-path test. PostgreSQL is not required for the skeleton. |

The [handler tests](internal/httpapi/handler_test.go) use `httptest` to check routes, method restrictions, and the current usage response without a running server. With a local Go toolchain, run `go test ./...`; the integration build tag keeps external API tests out of this command. `make go-test` runs the same package tests in a container without starting services.

The [SQL integrity tests](tests/sql/usage_inbox.sql) apply pending migrations, check schema integrity in UTC and `Asia/Shanghai`, and roll back their test data. Output identifies the suite and time zone being tested. Any test failure makes the command fail.

The [API happy-path test](tests/api/usage_batches_test.go) sends a valid Acme measurement to the running service and checks HTTP `202`, JSON content type, and `status: accepted`. It uses HTTP only, without importing the handler or asserting stub internals, so the same test can remain when validation and persistence are implemented. `make api-test` runs it in a temporary Go container on the Compose network; the API stays running for manual exploration.

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
- **Validation and retries:** database constraints and the future API's content comparison rules.

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

Every required inbox field must be supplied explicitly; the table has no database defaults. The planned billing API will set `received_at` from its UTC clock on first receipt and preserve the original value on identical retries. Application timestamps and month calculations use UTC.

The database rejects a repeated `(source, event_id)` and preserves the original row. The future ingestion API must compare the original content: an identical retry is accepted without another insert, while changed content for the same identity is a conflict. Blank text values, customer existence, and supported metrics will be validated by the application. The schema retains `NOT NULL`, the primary key, and checks for positive schema versions, non-negative units, valid intervals, and consistent processing state.

## Platform and billing contract

This section defines how the platform and billing service cooperate:

- [Event identity](#event-identity): source-wide uniqueness and stable IDs across retries and restarts.
- [Responsibility boundary](#responsibility-boundary): ownership of measurement, storage, delivery, durable receipt, and accounting.
- [Option C receipt and retry rules](#option-c-receipt-and-retry-rules): acknowledgements, retries, conflicts, and invalid input.
- [Agreements still open](#agreements-still-open): durability, retry windows, capacity, batch responses, freshness, and broker history.

**Status: working draft for option C, updated 9 October 2026.** Event identity is a confirmed design agreement; the delivery rules below remain proposals for the future API and simulator, not implemented HTTP guarantees. Record subsequent agreements and unresolved decisions in this section. The assignment makes the platform a separately owned measurement source and requires billing to handle retries, delays, and platform unavailability; it does not specify recovery from destruction of the platform's only storage copy.

### Event identity

The platform must assign each measurement an `event_id` that is unique within its `source` across all customers, sandboxes, producer instances, and restarts. The identity is `(source, event_id)`; `source` defines a stable namespace, not a namespace local to a customer or sandbox. Allocate the ID before the first send, for example using a UUID, and preserve both identity fields on retries, after restarts, and when regrouping events into batches. Never reuse an identity for a different measurement.

`customer_id` and `sandbox_id` are part of the event content, not the identity. An identical retry succeeds without another insert; the same identity with changed content, including a different customer or sandbox, is a conflict. This comparison will be implemented by the future billing API; the current database primary key only prevents duplicate rows.

### Responsibility boundary

| Area | Owner | Proposed agreement |
| --- | --- | --- |
| Measurement and sender storage | Platform team | Generate measurements using the [usage schema](#usage-inbox-contract), assign stable identities, and durably store events before the first send. Keep unacknowledged events outside the sandbox lifecycle. |
| Delivery and sender recovery | Platform team | Retry unacknowledged events with the same identity and content. Resume pending delivery after restart when sender storage survives. Changing batch boundaries does not change event identities. |
| Durable receipt | Billing team | Validate and compare input, commit accepted measurements to PostgreSQL, and acknowledge only identities with a durable receipt. Preserve committed input even if the response is lost. |
| Accounting after receipt | Billing team | Process each identity with a single financial effect. Financial writes and the inbox completion marker commit together under the customer lock; failed work remains recoverable. |
| Interface and operational guarantees | Both teams | Agree on metric meaning, supported schema versions, response semantics, permitted delays, failure coverage, and overload behaviour. Neither team can infer the other team's durability guarantee from the transport alone. |

Billing's responsibility begins at the durable inbox commit. The platform may release its sender copy only after receiving the matching acknowledgement. A lost response deliberately leaves overlapping copies; retry must not create another financial effect.

Recovery of measurements lost on the platform before a billing commit, including destruction of the only sender disk, belongs to the platform team and is outside the billing MVP implementation. This boundary does not declare such loss acceptable: the platform's actual loss tolerance and failure coverage remain an explicit agreement. Billing cannot reconstruct measurements it never received without another authoritative source.

The local billing setup covers restarts with the PostgreSQL volume intact. Replicated billing storage and recovery from destruction of that volume are not implemented.

### Option C receipt and retry rules

| Outcome | Meaning | Platform action |
| --- | --- | --- |
| `202` identifying accepted events | Those identities have committed inbox receipts. An identical retry acknowledges the original receipt without another insert or financial effect. Acceptance does not mean accounting has completed. | Record delivery for those identities, then allow sender cleanup. |
| Timeout, lost connection, or transient unavailability | Durable receipt has not been confirmed to the sender; a timeout can also occur after a successful commit. | Keep the events and retry with the same identities and contents, using backoff and jitter. |
| Existing identity with changed content | A conflict; the original measurement and its financial effect are preserved. | Retain the unresolved input for investigation; do not treat it as delivered or repeatedly submit changed content under that identity. |
| Invalid or unsupported input | The affected input was not accepted. | Keep a visible failure record and resolve the contract or data error before resubmission. |

The durable-receipt promise comes from this application contract, not from HTTP `202` alone. The current skeleton proposes `status: accepted` for the successful response. Additional response fields, errors, and whole-batch versus per-event failure behaviour remain open below.

Arrival order is not consumption-time order. The sender preserves the original measurement interval; billing resolves prices by consumption time and handles late usage without changing issued invoices. Credit changes and closing still require coordinated customer transactions.

### Agreements still open

| Decision | Owner | To specify |
| --- | --- | --- |
| Platform durability | Platform team | Failure coverage, allowed data loss, and recovery of the first measurement record; no unconditional loss-free guarantee is assumed. |
| Outage and retry window | Both teams | Maximum expected unavailability, delay, automatic retry age, and matching deduplication retention. |
| Sender capacity and overload | Platform team | Buffer capacity and the action when no durable write is possible; existing measurements must not be silently discarded. |
| Batch acknowledgement | Both teams | Supported schema versions, batch limits, response format, and atomic versus partial acceptance. |
| Spend-status freshness | Both teams | Polling interval and how pending input or unavailable status affects platform decisions. |
| Broker history for growth | Both teams | Whether replay after successful accounting, independent consumers, and a raw archive are needed. Pending work must survive until durable processing or handoff; post-processing broker history is a separate choice. |

## Architecture and HTTP interfaces

![Option C: building blocks and interfaces](docs/diagrams/option-c-components/option-c-components.png)

[Native Mermaid source](docs/diagrams/option-c-components/option-c-components.mmd).

The highlighted inbox schema is implemented, and the Go API now has the skeleton described above. The remaining blocks and ingestion behavior describe the intended architecture. API and worker modules may initially share one Go process. The two storage blocks represent tables in one PostgreSQL database.

| Interface | Status | Purpose |
| --- | --- | --- |
| `GET /healthz` | Implemented | Confirm HTTP server readiness. |
| `POST /usage/batches` | Response stub | Explore the request and response. Planned: validate and store usage, returning `202` after the inbox transaction commits. |
| `GET /customers/{id}/spend-status` | Planned | Return the current UTC month, processed gross spend, limit status, and processing lag. |

An accepted event may still await accounting. The worker will claim pending rows through SQL, apply prices and credit under the customer lock, and commit the financial effect with the inbox completion marker in one transaction. Monthly closing must wait for the customer's fixed boundary of accepted input before issuing an immutable invoice. The platform polls spend status independently of sending new usage.

Keep the architecture diagram's Mermaid source and PNG in sync when changing it. Documentation generation tools are local and excluded from the repository.
