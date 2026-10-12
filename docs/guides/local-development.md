# Local setup

## Requirements

Docker with Docker Compose and Make. The PostgreSQL client and Go toolchain run inside containers; no local Go installation is required. Run all `make` commands from the repository root.

## Get the source

If you do not already have a local checkout, clone the repository:

```sh
git clone https://github.com/pupitooo/e2b-billing-api.git
cd e2b-billing-api
```

## Configuration

Docker Compose reads an optional local `.env` file. The defaults are sufficient for local development. Copy [.env.example](../../.env.example) to `.env` to change the API port (`E2B_API_PORT`, default `8081`), documentation portal port (`E2B_DOCS_PORT`, default `8082`), PostgreSQL port, or development password. Keep the same configuration for subsequent commands.

## Initialize the database

On first setup, start PostgreSQL and apply the schema:

```sh
make up SERVICE=postgres
make migrate
```

The database is now ready for connections. Run migrations again when an update introduces schema changes; see [Database migrations](migrations.md#database-migrations). An existing database with all migrations applied needs no initialization on restart.

## Running services

Start all implemented services:

```sh
make up
```

`make up` starts PostgreSQL, the API, worker, the documentation portal, Scalar API reference, Adminer, and the simulator container and waits for readiness. The simulator waits for an explicit `make simulate` command before creating usage. Startup does not apply database migrations. The API verifies its database connection on startup; ingestion returns `503` until the inbox migration has been applied. The worker connects to PostgreSQL independently of the API; migrate explicitly before accounting can proceed.

`make simulate` launches a separate CLI process with `--no-deps`; it does not
start PostgreSQL, the API, or the worker. Start and migrate the selected project
before sending a scenario.

`make up`, `make docs`, `make restart`, and `make ps` print the actual browser addresses of running HTTP services. Use `make links` to show them again.

| Command | Purpose |
| --- | --- |
| `make up` | Start all services and wait for readiness. |
| `make delorean 2026-11-01` | Delete project volumes, start all services at midnight UTC on the supplied date, and apply migrations. |
| `make delorean 2026-12-01 KEEP_TIMELINE=1` | Change demo time while preserving database history, simulator checkpoints, and browser progress. |
| `make up SERVICE=postgres` | Start PostgreSQL and wait for readiness. |
| `make up SERVICE=api` | Build and start the Go API and wait for readiness. |
| `make api-docs` | Start the portal and print the Scalar reference link. |
| `make docs` | Start the guides, Scalar reference, and API for browser requests. |
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
| `make services` | List available service names: `adminer`, `api`, `api-docs`, `docs`, `postgres`, `simulator`, and `worker`. |
| `make help` | Show all available commands. |

PostgreSQL uses the pinned `public.ecr.aws/docker/library/postgres:18.6-alpine` image, UTC timestamps, and a named volume. Its port is published on `127.0.0.1`. Database data survives `make stop`, `make restart`, and `make down`.

`E2B_POSTGRES_PASSWORD` initializes the role password when the volume is empty. Changing the environment variable later does not update the password in an existing database.

## Reset a demo with DeLorean

Named after the time machine in *Back to the Future*, this command accepts a
positional calendar date and fixes demo time at midnight UTC:

```sh
make delorean 2026-11-01
```

It validates the date before running `docker compose down --volumes --remove-orphans`,
then runs `make up` for all services and `make migrate`, sequentially even with
`make -j`. It deletes the selected Compose project's database and simulator
checkpoints, including existing financial history. Browser walkthrough cursors
and request history remain in browser storage; reset them separately for a fresh run.
The date must use `YYYY-MM-DD`, with a year from 1000 through 9999.

Add `KEEP_TIMELINE=1` to keep the existing timeline while travelling to another
date:

```sh
make delorean 2026-12-01 KEEP_TIMELINE=1
```

This validates the date, stops API and worker, then runs `make up` and
`make migrate` sequentially. It preserves all project volumes, including invoices,
credit history and simulator checkpoints, plus the browser cursor and request
history. Use the same Compose project and resume the same scenario checkpoint.
Existing arrival times, accounting decisions, and issued invoices retain their
stored values. Advance time between completed scenario steps; changing the clock
does not replay earlier events. `KEEP_TIMELINE` accepts only `0` (the default
volume reset) or `1` (preserve history); other values fail before service changes.

The command supplies the selected date at midnight UTC through `E2B_DEMO_NOW`
to API, worker and portal for this invocation. It preserves project and port configuration and
starts all services regardless of `SERVICE`. It does not change the parent
terminal's environment or `.env`. Before a subsequent `make up`, export the same
`E2B_DEMO_NOW` to retain fixed time, or clear it to return to real time; see
[clock configuration](runtime-configuration.md#shared-business-clock).

## Compose projects and ports

A Docker Compose project groups this application's containers, network, and named
data volumes. The same checkout and `compose.yaml` can run multiple projects.
Each project has its own PostgreSQL database and simulator checkpoints: granting
credit in one project's GUI does not change another project's balance.

Without an explicit project selection, `compose.yaml` names the project
`e2b-local`. To select an isolated project for all commands in one terminal, use:

```sh
export COMPOSE_PROJECT_NAME=e2b-assignment-01
export E2B_API_PORT=8183
export E2B_DOCS_PORT=8182
export E2B_POSTGRES_PORT=5542
```

Use free host ports; these values are examples. Project selection determines the
network and stored data. The port variables determine where browsers and local
database clients connect; changing only ports does not select another project.
Inside the selected project's network, the simulator still calls `api:8080` and
application services connect to `postgres:5432`.

| Configuration | Selected resources or connection |
| --- | --- |
| `COMPOSE_PROJECT_NAME=e2b-local` with default ports | Development containers and data; GUI at `http://127.0.0.1:8082/demo/`. |
| `COMPOSE_PROJECT_NAME=e2b-assignment-01` with the example ports above | Independent containers and data; GUI at `http://127.0.0.1:8182/demo/`. |
| `E2B_API_PORT`, `E2B_DOCS_PORT`, `E2B_POSTGRES_PORT` | Host ports for the selected project's API, portal, and PostgreSQL. |

Keep the same project selection and port exports for `make up`, `make migrate`,
`make simulate`, `make ps`, `make links`, and service operations. Exports apply to
the current terminal and its child processes. In a new terminal, restore all four
values before continuing. To return to development, set
`COMPOSE_PROJECT_NAME=e2b-local` and restore its port values.

An explicit command such as
`make COMPOSE='docker compose -p e2b-assignment-01' ps` also selects that project;
`-p` takes precedence over `COMPOSE_PROJECT_NAME`. Without the environment export,
the override must be repeated on every command. Starting services with plain
`make up` and then selecting a different project only for `make simulate` leaves
the simulator without that project's API.

Follow the [isolated assignment setup](../simulator/billing-scenarios.md#start-an-isolated-assignment-environment)
for the complete migration, startup, simulation, and GUI sequence. Named volumes
survive `make down`; use a previously unused project name for another fresh run.

For missing API names, occupied ports, or pending usage_inbox rows, follow
[troubleshooting](troubleshooting.md).
