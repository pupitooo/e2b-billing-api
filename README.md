# E2B Billing API

Billing service for the E2B assignment, built with Go and PostgreSQL. It provides an HTTP API, an independently managed worker runtime, versioned database migrations, a usage inbox, an accounting model with the assignment's initial catalog, and a restartable platform simulator.

The project uses the selected [option C architecture](docs/brainstorming/architecture-options.md#why-option-c-was-selected). The [architecture comparison](docs/brainstorming/architecture-options.md) records the design rationale.

The [billing model guide](docs/architecture/billing-model.md) describes customers, price history, credit records, rated usage, monthly spend, add-ons, and seed data. The [financial rules](docs/architecture/accounting-rules.md) define the shared Go calculations, exact credit before rounding, and transaction/closing contract. Transactional accounting, financial APIs, and immutable monthly invoices are implemented.

The [implemented PostgreSQL ERD](docs/diagrams/implemented-data-model/implemented-data-model.png) shows all 18 tables, columns, and foreign keys through migration 007, including rating links, closing cohorts, and immutable invoice records. Its [editable Mermaid source](docs/diagrams/implemented-data-model/implemented-data-model.mmd) accompanies the preview.

The [usage-to-invoice flow](docs/architecture/usage-to-invoice.md) maps usage events, rating, exact groups, credit, rounding, invoice lines, and invoices to database records, including exact tick credit and immutable invoice snapshots.

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

### API runtime budgets

The API reads configuration once at startup. Compose passes the settings below
from `.env` into the API container; restart with `make up SERVICE=api` after
changing them. A standalone binary reads the same environment variables.
Durations use Go units, such as `500ms`, `10s`, or `2m`. Missing values use the
defaults; malformed, nonpositive, or incompatible budgets prevent startup.

| Environment variable | Default | Purpose |
| --- | --- | --- |
| `E2B_API_STARTUP_TIMEOUT` | `10s` | Open and verify the database connection. |
| `E2B_API_READ_HEADER_TIMEOUT` | `5s` | Read HTTP headers. |
| `E2B_API_READ_TIMEOUT` | `15s` | Read the complete request, including its body. |
| `E2B_API_INGESTION_TIMEOUT` | `10s` | Acquire a pool connection and commit one validated batch. |
| `E2B_API_ROLLBACK_TIMEOUT` | `5s` | Clean up a failed transaction independently of request cancellation. |
| `E2B_API_WRITE_TIMEOUT` | `35s` | Write deadline set after request headers, including body reading and storage. |
| `E2B_API_IDLE_TIMEOUT` | `90s` | Reuse collector connections across minute-based reports with jitter. |
| `E2B_API_SHUTDOWN_TIMEOUT` | `45s` | Drain HTTP requests on SIGINT or SIGTERM before closing the database pool. |
| `E2B_API_STOP_GRACE_PERIOD` | `60s` | Compose's time before forcibly killing the container. |
| `E2B_API_DB_MAX_CONNS` | `8` | Maximum database connections per API process. |
| `E2B_API_DB_MIN_CONNS` | `2` | Minimum warm connections; zero is allowed. |
| `E2B_API_MAX_IN_FLIGHT_BATCHES` | `32` | Admitted batches per process, including body reading and pool waiting. |

The header deadline must fit within the read deadline. The write budget must
exceed read + ingestion + the configured rollback cleanup budget,
leaving time for an error response. The shutdown budget must exceed header +
write; Compose's stop grace period must exceed shutdown + rollback cleanup.
The application validates its own budgets; configure the container's stop grace
period separately when increasing shutdown or rollback time. These deadlines limit I/O and
database work; they do not forcibly terminate arbitrary handler CPU work.

When all admission slots are occupied, ingestion returns `503` / `Retry-After: 1`
before application body parsing. Health requests remain available. Accepted
batches still commit atomically; producers retain rejected events and retry
unchanged identities and content with backoff and jitter.

The API's explicit pool limits override pgx pool sizes in `DATABASE_URL`.
Budget the database across all API replicas, accounting workers, and
administration: their combined maximum connections must fit PostgreSQL's
connection limit. Customer count does not imply one database connection per
customer. Admission bounds decoded batch memory, with at most 32 MiB of raw
bodies under the existing 1 MiB limit, plus decoded objects and HTTP overhead.

### Workload assumptions and measurements

The assignment asks the design to consider approximately 50_000 customers,
each running from a few to thousands of sandboxes, with minute-based reports.
It does not prescribe an average sandbox count, batching topology, peak rate,
or latency target, and does not require the implementation to demonstrate that
capacity. For the following calculations, assume one metric and one event per
active sandbox per minute, coalesced by platform collectors across customers:

| Average active sandboxes per customer | Events/s | Requests/s at 1_000 events/batch |
| --- | --- | --- |
| 3 | 2_500 | 2.5 |
| 10 | 8_333.3 | 8.3 |
| 100 | 83_333.3 | 83.3 |

Rate = customers × average active sandboxes × metrics / 60. More metrics and
retries multiply this rate. A separate batch from every customer each minute
would instead mean about 833 HTTP requests/s, even with only ten events each.
The 1_000-event maximum also remains subject to the 1 MiB body limit. Spread
minute reports with jitter; a synchronized burst and recovery backlog need
separate capacity measurements and producer buffering.

On 2026-10-09, a local Docker/Linux arm64 sample used 16 concurrent writers, private
schemas, two runs of 50 new batches, PostgreSQL 18.6, and the existing synchronous
commit path. For 1_000-event batches, p95 latency including pool waiting was
306–324 ms with 4 connections, 260–409 ms with 8, and 209–217 ms with 16.
Short samples vary; more connections do not guarantee lower latency.
Eight connections are the initial per-process budget: these observed latencies
leave room under the ten-second deadline while reserving connections for
other processes. It is not a sustained throughput or production-capacity claim;
it excludes HTTP parsing, duplicate comparisons, accounting, growing indexes,
minute bursts, and failover. Increasing the pool alone does not establish support
for billions of monthly records.

Reproduce the short storage sample after starting PostgreSQL with
`make up SERVICE=postgres` (all benchmark data lives in owned temporary schemas):

```sh
docker compose build api
docker compose run --rm --no-deps api go test -tags=integration -run '^$' \
  -bench '^BenchmarkPostgresInsertBatch$' -benchtime=50x -cpu=16 -count=2 ./tests/inbox
```

Tune deployments from measured p95/p99 acceptance latency, connection-acquisition
waits, CPU, disk/WAL behavior, overload responses, and accounting backlog. At
8_333 events/s with 1_000-event batches, a measured mean transaction time of
0.2 s would imply about 1.7 occupied connections on average; p95 is not the mean
and this example does not cover peaks. Validate representative payloads, retries,
worker competition, and sustained storage growth before changing budgets or
adding replicas. See the [architecture growth TODOs](docs/brainstorming/architecture-options.md#todo-higher-load-and-evolution-beyond-c).

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

`make up` starts PostgreSQL, the API, worker, Scalar documentation, and the simulator container and waits for readiness. The simulator waits for an explicit `make simulate` command before creating usage. Startup does not apply database migrations. The API verifies its database connection on startup; ingestion returns `503` until the inbox migration has been applied. The worker connects to PostgreSQL independently of the API; migrate explicitly before accounting can proceed.

`make up`, `make docs`, `make restart`, and `make ps` print the actual browser addresses of running HTTP services. Use `make links` to show them again.

| Command | Purpose |
| --- | --- |
| `make up` | Start all services and wait for readiness. |
| `make up SERVICE=postgres` | Start PostgreSQL and wait for readiness. |
| `make up SERVICE=api` | Build and start the Go API and wait for readiness. |
| `make docs` | Start Scalar documentation and the API for browser requests. |
| `make up SERVICE=worker` | Build and start only the standalone worker runtime. |
| `make stop SERVICE=worker` | Stop the worker while the API continues running. |
| `make restart SERVICE=worker` | Restart the worker without restarting the API. |
| `make logs SERVICE=worker` | Inspect worker startup, failures, and shutdown. |
| `make ps` | Show running and stopped services. |
| `make links` | Show browser links for running HTTP services. |
| `make logs SERVICE=postgres` | Show the last 100 PostgreSQL log lines. |
| `make restart SERVICE=postgres` | Restart PostgreSQL and wait for readiness. |
| `make stop` | Stop services while retaining containers and data. |
| `make down` | Remove containers and the network while retaining database and sender data. |
| `make services` | List available service names: `api`, `docs`, `postgres`, `simulator`, and `worker`. |
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
asynchronous in the standalone worker.

Measurements use `(source, event_id)` as identity. Identical retries succeed,
including duplicates within a batch, regrouped batches, and different timestamp
offsets for the same instant. Comparison includes schema version, customer,
sandbox, metric, both interval endpoints, and units. Retries preserve original
`received_at`, `processed_at`, and `processing_error`. Changed content returns
`409` and rolls back all new rows in that request. The optional `batch_id` is
accepted producer metadata; it is not stored or used for deduplication.

Requests require uncompressed UTF-8 `application/json`, one JSON document,
case-sensitive field names, and no unknown or duplicate members. The body limit
is 1 MiB (1_048_576 bytes), with 1–1_000 events and at most 256 UTF-8 bytes per
identifier or optional `batch_id`. Every required event field must be explicit
and non-null; zero units are valid. Versions and units use int32/int64 integer
tokens without decimal or exponent notation. Consumption times require valid
RFC 3339 calendar values, an explicit offset, and at most six fractional digits.
Identifiers are preserved and time instants normalize to UTC.

| Status | Behavior |
| --- | --- |
| `202` | Whole batch durably committed; identical existing measurements preserved. |
| `400` | Invalid or ambiguous JSON, incorrect types, or numeric decoding outside int32/int64 ranges. |
| `413` | The body exceeds 1_048_576 bytes. |
| `415` | Unsupported content type, charset, or compression. |
| `422` | Missing/null fields, invalid measurement values, or an event-count limit violation. |
| `409` | Different measurement content for one event identity, stored or repeated in the request. |
| `503` | Inbox unavailable, transaction canceled/timed out, or commit failed; outcome may be unknown. |

Errors use `{"error":{"code":"invalid_batch","message":"...","field":"events[0].units"}}`,
with `field` included when a validation location is available. A `409` uses code
`event_conflict` and identifies `source` and `event_id`; retain that input for
investigation. Correct invalid requests before retrying.

Database work has a configurable deadline (10 seconds by default), including
pool waiting, and honors request cancellation. Admission exhaustion also returns
`503` before application body parsing. A `503`
uses code `inbox_unavailable` with `Retry-After: 1`, without exposing SQL details.
Retain events and retry the same identities and content after backoff. A `503`
or a lost response may occur after commit; idempotence makes unchanged retries
safe. See the [OpenAPI specification](docs/api/openapi.yaml) for the full contract.

## Platform simulator

The separate Go executable [cmd/platform-simulator](cmd/platform-simulator/main.go)
creates synthetic measurement increments and sends them through the actual
`POST /usage/batches` interface. It supplies no prices or monetary charges.
The named `simulator_data` volume retains the plan, stable identities, release
cursor, and delivery receipts across commands and container restarts.

Start the API after [applying migrations](#initialize-the-database):

```sh
make up SERVICE=api
make simulate SCENARIO=assignment MODE=step
make simulate SCENARIO=assignment MODE=step
make simulate ACTION=status
```

The first command releases the two October 10 measurements; the second releases
the two October 20 measurements. Each `MODE=step` invocation releases at most
one step. `MODE=fast` (the default) releases the current phase at once, stopping
at the same barrier before late October. No command waits for the real calendar
to reach the fixture dates, and faster delivery never changes consumption times.

After October accounting and invoice issuance, explicitly release late usage:

```sh
make simulate ADVANCE=1 MODE=step
make simulate MODE=step
```

These commands deliver Acme's October 30 measurement and then November 3.
`make simulate ADVANCE=1 MODE=fast` releases both together. The barrier records
operator intent; the simulator cannot check invoice or accounting completion.
The legacy transport scenario uses an operator barrier. The separate
[public billing scenarios](docs/simulator/billing-scenarios.md) automate accounting,
credit, add-on purchases, invoice issuance, and platform monthly-status reads through
the documented financial APIs. HTTP `202`
confirms the whole batch's durable inbox receipt, not financial processing.

Run the complete billing assignment against fresh seeded accounts with the API and
worker running:

```sh
make simulate SCENARIO=billing-assignment STATE=/state/billing-assignment.json
```

Its named JSON steps declare requests and literal expected responses together,
including exact invoice lines, credit ticks, and faults after committed replies.
The [billing scenario guide](docs/simulator/billing-scenarios.md) lists the remaining
workflows and commands for outage recovery in a new process.

The default scenario reproduces these exact hourly totals:

| Consumption hour (UTC) | Acme `cpu_seconds` | Cyberdyne `cpu_seconds` |
| --- | ---: | ---: |
| October 10, 2026, 12:00 | 100_000_000 | 123_456_789 |
| October 20, 2026, 12:00 | 200_000_000 | 200_000_000 |
| October 30, 2026, 12:00 (late) | 50_000_000 | — |
| November 3, 2026, 12:00 | 100_000_000 | — |

### Generate, pause, resume, and replay

```sh
make simulate ACTION=generate MODE=step
make simulate ACTION=status
make simulate ACTION=send
make simulate ACTION=replay BATCH_SIZE=1 REVERSE=1
```

`generate` saves a step without contacting billing, so generation and delivery
can be controlled separately. `send` drains only previously released pending
measurements. `replay` resends all released measurements, including confirmed
ones, with unchanged identities and content. It releases no future steps.
An ordinary `run` after an interrupted delivery first drains its existing
pending buffer without advancing the scenario; invoke it again to continue.
Status reports released steps, generated, pending and delivered measurements,
HTTP attempt count, the next barrier, and the last delivery error.

The complete plan is saved before sending, and each released step is persisted
before its first request. Each receipt uses a synced atomic file replacement.
An OS file lock permits one writer per state file; read-only status remains
available during retries. Ctrl+C or a killed process releases the lock.
Timeouts, lost responses, `429`, and server errors retain
measurements and retry with increasing delay and jitter, honoring `Retry-After`.
Other responses, including `409` and invalid acknowledgements, stop with the
buffer retained for investigation. Default retries continue until interrupted;
`MAX_ATTEMPTS` can bound attempts per batch, including deliberate duplicates.

The guarantee begins after successful storage and assumes the sender volume
survives. Disk loss is outside this local simulator's guarantee. Keep
`simulator_data` together with its database: if PostgreSQL is reset while sender
receipts remain, use `ACTION=replay` to restore released events to the inbox.
Deleting only sender state and changing identities can add consumption again.

### Controls and fault scenarios

| Make parameter | Default | Effect |
| --- | --- | --- |
| `SCENARIO` | `assignment` | `assignment`, `lost-response`, `duplicates`, or `custom`. |
| `ACTION` | `run` | `run`, `generate`, `send`, `status`, or `replay`. |
| `MODE`, `ADVANCE` | `fast`, `0` | Release a phase or one step; explicitly pass a barrier. |
| `SOURCE`, `STATE` | `platform-simulator`, `/state/run.json` | Stable namespace and persistent run file. |
| `SANDBOXES`, `INTERVAL` | `1`, `1h` | Split hourly assignment totals exactly across sandboxes and intervals. |
| `BATCH_SIZE`, `DELAY` | `100`, `0s` | Events per batch and delay between successful batches. |
| `TIMEOUT` | `15s` | Timeout of each network attempt. |
| `RETRY_MIN`, `RETRY_MAX` | `1s`, `30s` | Initial and maximum backoff; `Retry-After` remains a minimum. |
| `MAX_ATTEMPTS` | `0` | Zero means retry until interrupted; positive values stop with pending data. |
| `DUPLICATES`, `LOSE_RESPONSE`, `REVERSE` | `0`, `0`, `0` | Extra identical copies, one ignored success per saved run, or reversed delivery. |
| `SIM_API_URL` | `http://api:8080` | Billing base URL from inside the simulator container. |
| `SCENARIO_FILE` | `/scenarios/custom-scenario.json` for `custom` | Operator scenario mounted from `docs/simulator/`. |

```sh
make simulate SCENARIO=lost-response MODE=step
make simulate ACTION=replay SCENARIO=duplicates BATCH_SIZE=1
```

The fault names reuse the assignment plan and saved identities. `lost-response`
ignores the first valid `202` once per saved run; its retry exercises acceptance
after a commit whose acknowledgement was lost. `duplicates` sends one extra
identical copy per batch. Once all released events are confirmed, use `replay`
to send them again. To demonstrate downtime, stop the API, release a step and
observe pending retries, then restart the API from another terminal:

```sh
make stop SERVICE=api
make simulate MODE=step
# In another terminal:
make up SERVICE=api
```

The service must already be initialized and running before delivery; simulator
commands deliberately do not start a stopped API. `make simulate-help` lists
the executable's flags. With a local Go toolchain, the same executable runs as
`go run ./cmd/platform-simulator --api-url=http://127.0.0.1:8081 --state=/tmp/e2b-sender/run.json`.
The file lock requires Linux or macOS, as provided by the Compose container.

### Custom scenarios and measurement splitting

Edit the tracked [custom scenario example](docs/simulator/custom-scenario.json)
or add a local JSON file in the same directory. Each step has a unique `name`,
an optional operator `barrier`, and an `events` array with every measurement
field explicitly supplied except `source`, which comes from `SOURCE`.
Unknown fields, null or missing event values, invalid measurements, and repeated
event identities are rejected before delivery. Explicit zero units are valid.

```sh
make simulate SCENARIO=custom SOURCE=custom-example STATE=/state/custom.json MODE=step
make simulate SCENARIO=custom SOURCE=custom-example STATE=/state/custom.json ADVANCE=1
```

Every later `run` or `generate` must match the saved plan, including source,
identities, timestamps, splitting, and units. To explore another plan, select a
different state file and a distinct source intentionally; it creates additional
measurements for those customers. Use an isolated database for independent
experiments. `send`, `status`, and `replay` use the saved plan directly.

`INTERVAL` must divide one hour and lie between `1m` and `1h`; `SANDBOXES` is
between 1 and 1_000, and the resulting scenario is limited to 10_000 events.
Division distributes the integer remainder without changing any hourly total.
Each event represents an increment for its sandbox and interval, never a
cumulative counter. Both API limits (1_000 events and 1 MiB per request) are
respected even when long identifiers require smaller batches. Snapshot storage
is intended for small reproducible scenarios; it is not a measured load capacity
or a production metering implementation.

## API documentation

Run `make docs` and open the printed documentation address (by default
[http://127.0.0.1:8082](http://127.0.0.1:8082)). The single Scalar service uses
the `modern` layout and an embedded **Test Request** client. It serves the
[OpenAPI 3.1.2 specification](docs/api/openapi.yaml) and bundled assets locally,
with browser requests forwarded through the same-origin `/api` proxy in
[docs/api/Caddyfile](docs/api/Caddyfile). Refresh the page after editing the
mounted specification. Every interface change must update this specification;
implemented ingestion and asynchronous accounting semantics are described there.

## Standalone worker

The worker is a separate Go process and Compose service, with its own entry point
in [cmd/billing-worker/main.go](cmd/billing-worker/main.go). It shares the source
repository and container image contents with the API, but has its own process,
restart policy, resource limits, and logs. It has no published port and does not
require a running API. No cron or API request starts the processing loop.

```sh
make up SERVICE=worker
make logs SERVICE=worker
make restart SERVICE=worker
make stop SERVICE=worker
```

**Current behavior: transactional accounting.** The worker prices one accepted receipt
per transaction, applies exact credit to usage, updates original-month gross spend,
and commits inbox completion with the financial effects. Unsupported inputs retain
processing errors; transient storage failures roll back for retry. It runs independently
of the API with a bounded database pool. The heartbeat reports loop activity.

The [worker loop](internal/worker/worker.go) runs one batch at a time, starts
immediately, and continues without an idle delay when a processor reports more
work. Idle results and errors wait for the configured polling interval. Each
batch receives a deadline. SIGINT and SIGTERM cancel the loop and in-flight work;
the executable exits unsuccessfully if work ignores cancellation beyond the
shutdown timeout. It does not start a replacement loop in that process.

| Setting | Compose default | Purpose |
| --- | --- | --- |
| `E2B_WORKER_POLL_INTERVAL` | `1s` | Delay after an idle or failed iteration. |
| `E2B_WORKER_BATCH_TIMEOUT` | `5s` | Cooperative deadline for one processing batch. |
| `E2B_WORKER_SHUTDOWN_TIMEOUT` | `5s` | Maximum wait for the loop after a termination signal. |
| `E2B_WORKER_HEARTBEAT_MAX_AGE` | `15s` | Maximum allowed heartbeat age; must exceed batch timeout plus polling interval. |
| `E2B_WORKER_HEARTBEAT_FILE` | `/tmp/billing-worker-heartbeat` | Writable heartbeat path shared by the worker and its health probe. |
| `E2B_WORKER_STOP_GRACE_PERIOD` | `10s` | Compose termination grace period; keep it longer than the shutdown timeout. |

The Go executable has no configuration defaults: all four durations and the
heartbeat path must be supplied through environment variables. Missing, empty,
invalid, or incompatible settings prevent startup, including healthcheck mode.
Compose supplies the local defaults shown above.

Copy the settings from [.env.example](.env.example) to `.env`, then run
`make up SERVICE=worker` to apply changes. The local worker has a 0.5 CPU and
128 MiB memory limit. Production limits and replica counts require workload
measurement; no sustained processing capacity has been established.

Compose uses `billing-worker healthcheck` to check the loop's heartbeat in
`/tmp/billing-worker-heartbeat`. The file is replaced atomically and removed on a
clean exit. When running the binary outside Compose, `E2B_WORKER_HEARTBEAT_FILE`
must select a writable file for each process, and all duration settings must
also be exported. Health checks report loop activity,
including idle and error iterations; they do not confirm financial processing or
database readiness. A stuck processor eventually makes the heartbeat stale.
Docker's restart policy restarts an exited container; an unhealthy status alone
does not trigger a restart. Production orchestration should monitor loop health, backlog size, oldest pending input, and failures.

API and worker resource lifecycles are independent. Their PostgreSQL connections share the database with bounded pools and short
transactions. Restarting the worker leaves committed input available for processing.
Concurrent workers serialize financial effects under the customer account lock.

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

The tables include `usage_inbox` (received events), `schema_migrations` (migration versions), and the [billing model tables](docs/architecture/billing-model.md#tables-and-relationships). Inspect the inbox's columns, types, constraints, and indexes:

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

### Database migrations

With PostgreSQL running, apply pending migrations after first setup or an update that adds migrations, then inspect the applied versions:

```sh
make migrate
make migration-status
```

[migrations/migrate.sql](migrations/migrate.sql) is the migration entry point. It acquires a transaction-scoped advisory lock, applies pending migrations, and records each version in `schema_migrations`. Schema changes and version records commit together; a SQL error aborts the transaction. Repeating the command skips applied versions, including when several runners start concurrently.

Migration [001_usage_inbox.sql](migrations/001_usage_inbox.sql) creates the inbox and its partial index for pending, error-free input. Migrations are explicitly invoked, so they also run against an existing Docker volume; restarting the container does not apply them.

Migration [002_billing_model.sql](migrations/002_billing_model.sql) creates the accounting tables. [003_assignment_seed.sql](migrations/003_assignment_seed.sql) loads Acme, Cyberdyne, the historical prices, and `concurrency_pack`. Account balances start at zero with no spend limit. The seed is versioned and runs once; repeating `make migrate` preserves existing data.

Migration [004_exact_credit.sql](migrations/004_exact_credit.sql) converts credit balances, group allocations, and signed ledger amounts from whole cents to exact ticks. One cent becomes 1_000_000 ticks, preserving existing history. A legacy allocation exceeding exact gross usage stops the migration atomically; see the [migration guidance](docs/architecture/accounting-rules.md#migration-and-verification) before upgrading existing financial records.

To extend the schema, add the next numbered SQL file and a corresponding version check, include, and version record in `migrate.sql`. Once a migration is released, keep it unchanged. The initial migration creates the usage inbox schema.

## Project layout

- `cmd/billing-api/`: application entry point and server setup.
- `cmd/billing-worker/`: standalone worker startup, configuration, and signal handling.
- `cmd/platform-simulator/`: separate platform CLI entry point.
- `internal/`: private application packages, with `*_test.go` package tests next to the code.
- `migrations/`: numbered SQL migrations and the explicit PostgreSQL migration runner.
- `tests/api/`: HTTP integration tests against a running API, enabled with the `integration` build tag.
- `tests/inbox/`: PostgreSQL repository tests in isolated temporary schemas, enabled with the `integration` build tag.
- `tests/worker/`: executable lifecycle and exec health-check tests, enabled with the `integration` build tag.
- `tests/sql/`: database integrity tests executed with `psql`.
- `docs/`: project and interface documentation.

## Go formatting and static checks

Enable the tracked [pre-commit hook](.githooks/pre-commit) once after cloning:

```sh
make install-hooks
```

Every subsequent normal commit checks the staged snapshot with `gofmt`, the
`wsl` whitespace rules, the project's numeric-literal rule, and `go vet`, including integration-tagged test
code. A formatting violation or vet finding stops the commit. The hook preserves
partial staging and never formats or stages files automatically. Fix reported problems, then stage the intended
changes and commit again.

| Command | Behavior |
| --- | --- |
| `make fmt` | Group decimal numeric literals, preserve plain calendar years, and apply `gofmt` plus `wsl` whitespace fixes. |
| `make fmt-check` | Check `gofmt`, `wsl`, and numeric-literal grouping; change no files. |
| `make vet` | Run `go vet` for `cmd/`, `internal/`, and `tests/` with default and integration build tags. |
| `make check` | Run `gofmt`, `wsl`, numeric-literal, and vet checks together, as CI does. |
| `make install-hooks` | Set this clone's `core.hooksPath` to the tracked `.githooks` directory. |

These commands use installed Go when available, otherwise Docker builds the
`go-tools` stage from the existing [Dockerfile](Dockerfile). No running API or
database is needed. `E2B_GO_CHECKS_DOCKER=1 make check` explicitly uses Docker;
CI uses this mode to match the pinned project toolchain. Local private `tools/`
worktrees and dependency directories are excluded from formatting.

The pinned `wsl` and Go analysis dependencies are defined in an isolated
[tool module](scripts/whitespace/go.mod); enabled checks are defined in
[scripts/whitespace.env](scripts/whitespace.env). Local commands and Docker use
the same versions and policy. They separate completed blocks and returns in longer blocks, keep error
checks next to their operations, and remove blank lines at block boundaries.
Declarations and their initialization may stay together. Both ordinary and
integration-tagged packages are checked. `make fmt` repairs these rules;
`make fmt-check`, the staged hook, and CI enforce them without editing files.
The first use with installed Go downloads the pinned tool and its dependencies.
Semantic grouping of preparation, calculation, persistence, and assertions still
requires review. See the [wsl documentation](https://github.com/bombsimon/wsl).

The [numeric-literal checker](cmd/number-format/main.go) requires underscore
groups of three for decimal Go literals with five or more digits, for example
`10_000` and `1_000_000`. Fractional digits group from the decimal point, as in
`0.000_000_01`. Smaller literals may remain plain; existing separators must use
the same grouping. Calendar years stay plain, including `2026`, year-named values,
the year argument to `time.Date`, and comparisons with `Year()`. The checker reads
Go syntax, so comments, string values, dates inside strings, and non-decimal
literals are preserved. Markdown prose and calculation examples follow the same
number style; years, dates, identifiers, URLs, and copyable language examples
retain their required syntax. `make fmt` repairs Go literals; checks report the
file, line, and required spelling without editing files.

Hook configuration is local Git metadata and must be enabled in each clone.
Git permits bypassing local hooks with `--no-verify`; CI independently runs the
same checks on every push and pull request. The standard tools are documented
in [gofmt](https://pkg.go.dev/cmd/gofmt) and [go vet](https://pkg.go.dev/cmd/vet).

## Testing

`make test` is the primary test command. Start PostgreSQL with `make up SERVICE=postgres` before running all tests or the database suite; the Go suite builds and starts the API and worker automatically.

Follow the [final acceptance test procedure](docs/guides/final-acceptance-testing.md) to verify every assignment requirement, walk through the October/November example with explicit checkpoints, inspect persisted accounting state, and exercise outages, retries, and restart in isolated Compose projects.

| Command | Purpose |
| --- | --- |
| `make test` | Run all test suites. |
| `make go-test` | Run Go package tests without starting external services. |
| `make db-test` | Run only the database integrity suite. |
| `make api-test` | Start PostgreSQL, migrate, and run HTTP/database acceptance tests against the API. |
| `make inbox-test` | Start PostgreSQL and run repository tests in private schemas. |
| `make test SUITE=go` | Run Go unit, PostgreSQL repository, HTTP acceptance, and worker lifecycle tests; services start automatically. |
| `make test SUITE=db` | Run only the database integrity suite in both configured time zones. |
| `make test RUN='^TestPostUsageBatches$/^usage_batches_happy_path$'` | Run only the named Go scenario. Supplying `RUN` selects the Go suite by default. |

`SUITE` accepts `all` (the default), `go`, or `db`. `RUN` uses the standard [Go `-run` regular-expression filter](https://go.dev/src/cmd/go/internal/test/test.go): anchors select an exact test name; a pattern such as `Usage` selects matching names. Explicit `SUITE=all RUN=Usage` runs the full SQL suite and matching Go tests. Unknown suites and `SUITE=db` combined with `RUN` fail before starting test work.

The [handler tests](internal/httpapi/handler_test.go) use `httptest` to check routes, method restrictions, and the current usage response without a running server. With a local Go toolchain, run `go test ./...`; the integration build tag keeps external API tests out of this command. `make go-test` runs the same package tests in a container without starting services.

The SQL suite applies pending migrations and discovers all `tests/sql/*.sql` files. [Inbox tests](tests/sql/usage_inbox.sql), [billing model tests](tests/sql/billing_model.sql), and [assignment seed tests](tests/sql/assignment_seed.sql) run in UTC and `Asia/Shanghai` and roll back their data. Seed checks use a private schema, preserving edited application data. Output identifies the file and time zone; any failure makes the command fail.

The original [API happy-path request](tests/api/usage_batches_test.go) and response
expectations remain unchanged. The [acceptance tests](tests/api/usage_batches_test.go)
send HTTP requests to the running service and inspect committed rows through
a separate PostgreSQL connection. They cover durable receipt, preserved retry
metadata, atomic conflicts, invalid later events, and concurrent HTTP retries.
They remove only their owned rows, retaining any pre-existing fixed happy-path
fixture. The [repository tests](tests/inbox/usage_inbox_test.go) additionally
exercise deterministic lock waits, competing commits/rollbacks, canceled
transactions, and each conflicting content field. Each test drops its private
schema. The API stays running for exploration; `-count=1` executes every test.

Worker package tests cover loop cancellation, deadlines, retry pacing, heartbeat
health, and bounded shutdown. The [worker process tests](tests/worker/lifecycle_test.go)
start the compiled executable against PostgreSQL without an API dependency, verify its
health probe, send SIGTERM, and require a clean exit with heartbeat cleanup. They
also reject invalid startup configuration. Run them through `make test`, or
filter with `make test RUN='^TestWorkerExecutable$/^worker_process_lifecycle$'`.

Simulator unit tests verify exact fixture totals, splitting, barriers, durable
recovery, file locking after a killed process, safe retries, and retained errors.
The [simulator HTTP acceptance tests](tests/api/simulator_test.go) launch the
separate executable against the running API, inspect committed PostgreSQL rows,
and remove only their owned producer namespaces. Run them with
`make test RUN=Simulator`; they also run automatically in `make test` and CI.
The [public billing workflow tests](tests/inbox/billing_simulator_test.go) use the
same CLI with a real HTTP router, accounting worker, and private seeded schema per
scenario. They verify complete literal invoices and balances, monthly limit reads,
HTTP `503`/`Retry-After`, lost committed replies, and restart with retained state.

With Go 1.27 or later installed locally, the same test can target a running API directly:

```sh
E2B_API_URL=http://127.0.0.1:8081 \
E2B_TEST_DATABASE_URL='postgres://e2b:e2b_local_dev@127.0.0.1:5432/e2b_billing?sslmode=disable' \
go test -tags=integration -count=1 -v ./tests/api ./tests/inbox
```

[CI](.github/workflows/ci.yml) runs `make check` before `make test` on every push and pull request, using a fresh PostgreSQL volume and the same Compose configuration. The required `All tests` check includes Go formatting and static analysis, SQL integrity tests, Go package tests, PostgreSQL repository tests, HTTP integration tests, and worker lifecycle tests; the branch must also be up to date with `main`. Failed runs include service logs, and each run removes its test containers and volume.

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
UTC years from 1000 through 9999, use at most microsecond precision, and increase
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
| `POST /prices` | Append default or customer-specific historical price versions without invalidating rated history. |
| `POST /customers/{customer_id}/credits` | Apply an E2B grant once per operation identity. |
| `GET /customers/{customer_id}/credit` | Read exact credit ticks and accounting progress. |
| `POST /customers/{customer_id}/addons` | Purchase a recurring add-on with its price snapshot. |
| `POST /customers/{customer_id}/spend-limit` | Set or remove the monthly gross-usage limit idempotently. |
| `GET /customers/{customer_id}/limit-status` | Let the platform read the current UTC month's status. |
| `GET /customers/{customer_id}/months/{month}/limit-status` | Read an explicit original usage month with the current limit configuration. |
| `POST /customers/{customer_id}/invoices` | Close an explicit month, drain its durable cohort, and return one immutable numbered invoice. |
| `GET /customers/{customer_id}/invoices/{month}` | Read the issued buyer and financial snapshot. |

Keep the architecture diagram's Mermaid source and PNG in sync when changing it. Documentation generation tools are local and excluded from the repository.

## Transactional usage accounting

The standalone worker rates one receipt per transaction under the customer's account lock.
Historical pricing, cumulative group rounding, exact usage-only credit, original-month gross spend,
and inbox completion commit together. Concurrent workers and identical replay cannot charge twice.
Migration `005_accounting_processing.sql` adds immutable closed-month records for late-usage routing.

Unsupported customers, schemas, prices, and intervals are retained with `processing_error`.
Database failures roll back for retry. After diagnosing and correcting an unsupported input's
catalog or configuration, an operator may explicitly release that owned receipt:

```sql
UPDATE usage_inbox SET processing_error = NULL
WHERE source = 'investigated-source' AND event_id = 'investigated-event'
  AND processed_at IS NULL;
```

A receipt must fit within one UTC month and one applicable price version; units are never
spread across boundaries by assumption. Accounting retains exact ticks and rounds only
cumulative groups. The worker reserves two database connections per process.

### Price versions

`POST /prices` appends an immutable price version using its stable `price_version_id`.
Supply `customer_id: null` for the default, or a customer ID for an override. An identical
retry succeeds; changed content, duplicate effective instants, and retroactive changes
that invalidate already rated usage return `409`. Pending usage can use newly added historical
prices. See the [OpenAPI reference](docs/api/openapi.yaml) for explicit fields and examples.

### Credit

E2B calls `POST /customers/{customer_id}/credits` with an explicit operation ID, positive
`amount_cents`, and audit `recorded_at`. Grants apply immediately under the account lock;
unchanged retries succeed and changed content returns `409`. Credit pays new usage only.
`GET /customers/{customer_id}/credit` exposes exact `credit_balance_ticks` and committed
pending/error counts. One cent is 1_000_000 ticks; receipt acceptance can precede accounting.

### Add-on purchases

`POST /customers/{customer_id}/addons` uses a stable `subscription_id` and snapshots the
monthly catalog price. The full charge starts in the UTC purchase month, including purchases
at month end, and recurs on subsequent invoices. One subscription per customer and add-on is
supported. Identical retries return the original snapshot; duplicate subscriptions and new
purchases that would change a closed month return `409`. Credit never pays add-ons.

### Spend limits and platform queries

Apply migration 006, then use `POST /customers/{customer_id}/spend-limit` with a stable
operation ID and nonnegative `limit_cents`, or explicit `null` for unlimited. Replaying
an older operation preserves a newer limit. `GET /customers/{customer_id}/limit-status`
selects the server's current UTC month; `GET /customers/{customer_id}/months/{month}/limit-status`
selects an explicit `YYYY-MM` usage month with the current configuration. Status compares
exact gross usage before credit, excludes add-ons, and exposes pending/error counts.
Reaching a limit does not discard or stop accounting for measured usage.

### Monthly invoices

Apply migration 007, then call `POST /customers/{customer_id}/invoices` with an explicit
`month` such as `2026-10`. Issue months in increasing order. An empty month is valid.
The customer/month identifies this operation; retry returns the same invoice and number.
`GET /customers/{customer_id}/invoices/{month}` reads the immutable snapshot.

Closing first stores its committed pending receipt cohort. Errors block issuance; a deadline
leaves resumable closing work. Retry after correcting an input's supported catalog and
explicitly releasing its processing error. New accepted receipts outside that cohort route
to another open month, even with an older receipt timestamp. Buyer details, lines, exact
audit ticks, group freezes, closure, and the customer's next number commit together.
Issuance never consumes credit again. Later grants and consumption cannot change an issued
invoice. A new purchase cannot affect a closing or closed month. Operators trigger invoices
explicitly through the API; an automatic calendar scheduler remains an extension.

The [scenario style guide](docs/simulator/scenario-style.md) describes the named-step format
used for durable platform workflows and literal expected results. Simulator transport tests
run as `TestSimulatorExecutable` with separate workflow subtests; `RUN=Simulator` still selects them.
