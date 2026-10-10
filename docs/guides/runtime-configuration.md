# API and worker runtime configuration

## API runtime budgets

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

## Worker lifecycle and configuration

The worker is a separate Go process and Compose service, with its own entry point
in [cmd/billing-worker/main.go](../../cmd/billing-worker/main.go). It shares the source
repository and container image contents with the API, but has its own process,
restart policy, resource limits, and logs. It has no published port and does not
require a running API. No cron or API request starts the processing loop.

```sh
make up SERVICE=worker
make logs SERVICE=worker
make restart SERVICE=worker
make stop SERVICE=worker
```

The worker performs transactional accounting independently of the API, with a
bounded database pool. See the [financial rules — Transaction and closing contract](../../docs/architecture/accounting-rules.md#transaction-and-closing-contract)
for atomic updates and the [recovery procedure](../guides/accounting-recovery.md#investigate-and-release-a-receipt)
for unsupported input. The heartbeat reports loop activity.

The [worker loop](../../internal/worker/worker.go) runs one batch at a time, starts
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

Copy the settings from [.env.example](../../.env.example) to `.env`, then run
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
The worker reserves two database connections per process.
