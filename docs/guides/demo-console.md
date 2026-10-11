# Billing demo console

## Start and open

Use the same local services and explicit migrations as the API:

```sh
make up SERVICE=postgres
make migrate
make up
make links
```

Open the printed **Billing demo GUI** link, by default
[http://127.0.0.1:8082/demo/](http://127.0.0.1:8082/demo/).
The same portal exposes [Adminer](http://127.0.0.1:8082/database/) and
[Scalar](http://127.0.0.1:8082/reference/). Actual links follow `E2B_DOCS_PORT`;
GUI database links are relative to the portal and need no port configuration.

`make docs` starts the static console, documentation, API, PostgreSQL, and Adminer.
Start `make up SERVICE=worker` as well to process submitted measurements. `make up`
starts all services, including the worker. The CLI simulator stays idle until
explicitly invoked.

## Runtime and source separation

The console consists of buildless HTML, CSS and JavaScript in the repository's
`demo/` directory. Caddy in the existing `docs` container serves `/demo/` and
forwards `/api/` to billing. No additional Go handlers, custom backend, frontend
build, or local Node installation is required to use the console.

Standard Adminer is the only added Compose runtime service. Its version and image
digest are pinned in `compose.yaml`; PostgreSQL support and the SQL editor come
from the stock image. Adminer is reachable through the loopback-bound portal,
without a separate published port. Its session and redirects retain the
`/database/` prefix. All financial commands go through the existing public API;
database reads and maintenance are separate manual SQL operations.

## Screens and workflows

Select a customer and a UTC month in the header. The directory lists the two
assignment seeds as reference data; it does not claim to be a live database list.
Use its Adminer link for all stored customers, or select another existing customer
by ID. There is no customer-creation API in the current application.

| Screen | Operations and verification |
| --- | --- |
| Overview | Read exact credit, consumption-month gross usage, limit state, pending counts and errors. |
| Usage | Submit a measurement or a JSON batch, load an example of late usage, and trace receipts to rated groups. |
| Prices | Insert default/customer versions at the server instant or a future timestamp; inspect live historical versions in SQL. |
| Credit | Grant credit as E2B, inspect balance, grants, debits and allocations. |
| Add-ons | Purchase a catalog add-on and inspect its stored subscription snapshot. |
| Limits | Set, raise or remove a limit; query the selected month or server's current month. Zero is a real limit; null means unlimited. |
| Invoices | Issue/retry a completed UTC month, retrieve its immutable snapshot, inspect exact audit values, and print the document. |
| Assignment demo | Execute each published assignment step and compare the actual response to its literal expected result. |
| Database | Open or preview prepared inspection statements for the selected context. |
| Service | Prepare scoped pending cleanup or release of one repaired error; export/clear browser checkpoints independently of database state. |
| Request history | Inspect original JSON and responses; retry a saved request with exactly the same bytes. |

The inspector shows method, path, body, status and reply. All API data and SQL
parameters are escaped for their output contexts. Displayed money uses exact
integer arithmetic; the GUI preserves int64 JSON tokens without converting them
to imprecise JavaScript numbers. Rounded account summaries also show exact tick
values through the API inspector and eight-decimal USD values. Invoice lines use
the server's stored cent presentation rather than calculating another invoice.

A `202` reply means durable receipt, not successful financial processing. Refresh
account state to observe the worker. Pending and error counts remain visible;
quarantined usage is not presented as processed spending.

## Retries and browser state

The browser saves the original request before sending it. Request history retains
the latest 100 entries in local storage. The context and assignment source/cursor
also survive reload on the same browser origin. A storage failure prevents a new
request from being sent if its original content cannot first be saved.

Use **Exact retry** to retain the original customer path, timestamps, event
identities, operation keys and JSON body. Use **New operation key** only for a new
command. Editing a form while retaining its old key can correctly produce `409`.
Timeout or connection failure leaves the commit outcome unknown; it is not proof
of rollback. Browser storage is a convenience for this demonstration, not a
replacement for the Go simulator's durable producer buffering.

The assignment runner loads the existing `billing-assignment.json` fixture from
the published portal. It advances only when the declared status and response match.
Accounting checks poll briefly with a visible stop control; failed expectations
retain the current step. For declared response-loss faults, the runner sends the
command, simulates losing its successful reply, and repeats the same original
request. This demonstrates server idempotency without causing a real outage.

## Fresh assignment data

The walkthrough expects fresh seeded Acme and Cyberdyne accounts, unchanged seed
prices, no prior purchases/invoices, and the literal versions and balances in the
fixture. GUI experimentation can change that state. A separate Compose project
isolates its named database and simulator volumes from the ordinary local stack:

```sh
make COMPOSE='docker compose -p e2b-gui-demo' \
  E2B_API_PORT=8181 E2B_DOCS_PORT=8182 E2B_POSTGRES_PORT=5542 up SERVICE=postgres
make COMPOSE='docker compose -p e2b-gui-demo' migrate
make COMPOSE='docker compose -p e2b-gui-demo' \
  E2B_API_PORT=8181 E2B_DOCS_PORT=8182 E2B_POSTGRES_PORT=5542 up
```

The commands presume `e2b-gui-demo` has no existing volume. Open the printed portal
on port `8182`. This uses the same Compose file and adds no permanent services to
the application architecture. Closing the stack with `down` retains its volumes;
use a new project name for another fresh run.

Only ended UTC calendar months can close. As of 10 October 2026, the first portion
of the fixed assignment can run, but its October/November invoices require the
server clock to reach `2026-12-01T00:00:00Z`. The GUI does not override clocks or
weaken this rule. The server, not the browser's eligibility hint, decides whether
a month can close. Unfinished month responses remain visible with `422`.

Late usage received after closure can also route according to receipt month;
reproducing the exact fixed November invoice requires the original workflow's
receipt-time assumptions. See [public billing scenario prerequisites](../simulator/billing-scenarios.md#run-the-complete-assignment)
and the [late usage contract](../architecture/accounting-rules.md#utc-months-limits-add-ons-and-late-usage).

## Database inspection and maintenance

Each **View in database** link opens a separate tab with SQL prefilled. Log in with:

- System: PostgreSQL
- Server: `postgres`
- Username: `e2b`
- Database: `e2b_billing`
- Password: the configured local database password (`e2b_local_dev` by default)

First login may redirect to the database overview; reopen the GUI query link after
login. Review the statement and press **Execute**. No GUI link automatically runs
SQL. Statements show raw audit values, consumption/billing months, pending/error
markers and operation identities. Bounded queries avoid dumping the entire inbox.

Maintenance accepts only an explicit `demo-ui/` source. Pending cleanup deletes
only matching unprocessed, error-free receipts with no rating. Stop the worker
first if a stable preview is needed; otherwise it can process a qualifying row
before cleanup, which will then leave that row intact. Error release requires an
exact event ID, no rating, and repair of the underlying cause before execution.
Neither action changes financial records, clears command keys, or resets invoices.
Follow [accounting recovery](accounting-recovery.md) for catalog repair.

**Reset walkthrough cursor** and **Clear local journal** affect browser state only.
They cannot reset financial history. Export original identities before clearing
local checkpoints if you need to retry unknown outcomes.

## Verification

```sh
make demo-check
make docs-check
make check
```

`make demo-check` uses an ephemeral standard Node container to run tests for exact
money/JSON, request construction, SQL escaping and scoping, saved retries and
assignment checkpoints. It is not another running service. Browser review should
also cover all screens, actual API replies, Adminer login/prefilled SQL, responsive
layout and invoice printing. Financial browser experiments belong in an isolated
schema or Compose project, not a user's existing demonstration data.
