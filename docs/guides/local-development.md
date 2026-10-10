# Local development and service operations

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

The database is now ready for connections. Run migrations again when an update introduces schema changes; see [Database migrations](../guides/database-operations.md#database-migrations). An existing database with all migrations applied needs no initialization on restart.

## Running services

Start all implemented services:

```sh
make up
```

`make up` starts PostgreSQL, the API, worker, the documentation portal, Scalar API reference, and the simulator container and waits for readiness. The simulator waits for an explicit `make simulate` command before creating usage. Startup does not apply database migrations. The API verifies its database connection on startup; ingestion returns `503` until the inbox migration has been applied. The worker connects to PostgreSQL independently of the API; migrate explicitly before accounting can proceed.

`make up`, `make docs`, `make restart`, and `make ps` print the actual browser addresses of running HTTP services. Use `make links` to show them again.

| Command | Purpose |
| --- | --- |
| `make up` | Start all services and wait for readiness. |
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
| `make services` | List available service names: `api`, `api-docs`, `docs`, `postgres`, `simulator`, and `worker`. |
| `make help` | Show all available commands. |

PostgreSQL uses the pinned `public.ecr.aws/docker/library/postgres:18.6-alpine` image, UTC timestamps, and a named volume. Its port is published on `127.0.0.1`. Database data survives `make stop`, `make restart`, and `make down`.

`E2B_POSTGRES_PASSWORD` initializes the role password when the volume is empty. Changing the environment variable later does not update the password in an existing database.
