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

These commands use the selected [Compose project](local-development.md#compose-projects-and-ports),
`e2b-local` by default. For clean assignment accounts, use the
[isolated assignment setup](../simulator/billing-scenarios.md#start-an-isolated-assignment-environment)
before experimenting; its GUI and Adminer show that project's separate database.

## Runtime and source separation

The console consists of buildless HTML, CSS and JavaScript in the repository's
`demo/` directory. Caddy in the existing `docs` container serves `/demo/` and
forwards `/api/` to billing. No additional Go handlers, custom backend, frontend
build, or local Node installation is required to use the console.

Adminer is the only added Compose runtime service. Its version and image
digest are pinned in `compose.yaml`; PostgreSQL support and the SQL editor come
from the stock image. A mounted [display plugin](database-operations.md#integer-display-in-adminer)
groups integer result values with underscores while preserving raw edits and
exports. Adminer is reachable through the loopback-bound portal,
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
| Prices | Insert default/customer versions using today or a future UTC date; inspect live historical versions in SQL. |
| Credit | Grant credit as E2B, inspect balance, grants, debits and allocations. |
| Add-ons | Purchase or cancel a catalog add-on and inspect its immutable activation periods. |
| Limits | Set, raise or remove a limit; query the selected month or server's current month. Zero is a real limit; null means unlimited. |
| Invoices | Issue/retry a completed UTC month, retrieve its immutable snapshot, inspect exact audit values, and print the document. |
| Assignment demo | Select the complete assignment or fixed-clock October invoice walkthrough; execute literal expectations with separate saved cursors. |
| Database | Open or preview prepared inspection statements for the selected context. |
| Service | Prepare scoped pending cleanup or release of one repaired error; export/clear browser checkpoints independently of database state. |
| Request history | Inspect original JSON and responses; retry a saved request with exactly the same bytes. |

The inspector shows method, path, body, status and reply. All API data and SQL
parameters are escaped for their output contexts. Displayed money uses exact
integer arithmetic; the GUI preserves int64 JSON tokens without converting them
to imprecise JavaScript numbers. The remaining credit and gross usage cards show
rounded USD totals with the exact eight-decimal USD amounts underneath. The API
inspector exposes microcent amounts in the API fields named `*_microcents`
(1 cent = 1_000_000 microcents). Invoice lines use
the server's stored cent presentation rather than calculating another invoice.

A `202` reply means durable receipt, not successful financial processing. Refresh
account state to observe the worker. Pending and error counts remain visible;
quarantined usage is not presented as processed spending.

On **Invoices**, the header initializes **Billing month · UTC** in the invoice
form. Both **Issue invoice** and **Load invoice** use the month currently entered
in that form and the selected customer. Editing the form month lets you issue
and then load the same snapshot without also changing the header. To inspect
that month's database records, select it in the header as well.
While the request is in flight, **Load invoice** changes to **Loading…** and is
disabled. A message beside the controls confirms the loaded invoice number and
month, or shows a failure. Loading an already displayed invoice keeps the same
immutable document; the confirmation makes a successful refresh visible.

## Retries and browser state

The browser saves the original request before sending it. Request history retains
the latest 100 entries in local storage. The context and assignment source/cursor
also survive reload on the same browser origin. A storage failure prevents a new
request from being sent if its original content cannot first be saved.

Price activation and add-on purchase forms use date inputs (`YYYY-MM-DD`). The
backend resolves these dates to midnight UTC and returns dates. A price may start
today if rated history is preserved, or on a future date; dates before today are
rejected after catalog locking. Add-on dates may select an open historical month
or a future month. The credit form has no audit timestamp field; the backend owns
that ledger value. Retained request history must use the new command format.
Saved requests are retained exactly and are not silently rewritten. If an old
command has an unknown commit outcome, keep its original key when correcting the
format; never create a new key to bypass a retry conflict.

Use **Exact retry** to retain the original customer path, timestamps, event
identities, operation keys and JSON body. Use **New operation key** only for a new
command. Editing a form while retaining its old key can correctly produce `409`.
Timeout or connection failure leaves the commit outcome unknown; it is not proof
of rollback. Browser storage is a convenience for this demonstration, not a
replacement for the Go simulator's durable producer buffering.

The walkthrough runner loads `billing-assignment.json` or `billing-demo-invoice.json` from
the published portal; the assignment [24-step table](../simulator/billing-scenarios.md#billing-assignment)
documents each assignment action and expected result. Each walkthrough retains its own cursor and captures when switching scenarios. It advances only when the declared status and response match.
Accounting checks poll briefly with a visible stop control; failed expectations
retain the current step. For declared response-loss faults, the runner sends the
command, simulates losing its successful reply, and repeats the same original
request. This demonstrates server idempotency without causing a real outage.

## Fixed-clock invoice demo

Use a previously unused Compose project and free ports, preserving the original
seed catalog. These commands create a separate database; choose another project
name for another fresh financial run:

```sh
export COMPOSE_PROJECT_NAME=e2b-clock-demo
export E2B_POSTGRES_PORT=25432 E2B_API_PORT=28081 E2B_DOCS_PORT=28082
export E2B_DEMO_NOW=2026-11-01T00:00:00Z
make up SERVICE=postgres
make migrate
make up
make links
```

Open that project's GUI, select **Assignment demo**, then **October invoice with
add-on**. The clock displays **SIMULATED UTC**. Execute all nine steps; this
scenario requires fresh Cyberdyne accounts and does not grant credit. It purchases
`concurrency_pack` for October, sends four measurements from two sandboxes,
waits for processing, closes October, and reads/retries the immutable invoice.
The [literal scenario and step table](../simulator/billing-scenarios.md#billing-demo-invoice)
show the expected **38.17 USD** total, two usage lines and the add-on.

The same JSON is runnable through the CLI:

```sh
make simulate SCENARIO=billing-demo-invoice SOURCE=clock-demo STATE=/state/clock-invoice.json
```

Run GUI or CLI on fresh accounts, rather than both on the same already modified
account. A new producer source isolates event identities, not financial state.
To return to real time, `unset E2B_DEMO_NOW` and recreate all three services with
`make up`. The clock metadata is supplied by the portal from startup configuration,
not by a Go endpoint; [clock configuration](runtime-configuration.md#shared-business-clock)
explains timestamp and runtime-timer responsibilities.

For the complete assignment, keep the November fixed clock through step 18,
including late October receipt and November accounting. Between steps 18 and 19:

```sh
make stop SERVICE=api
make stop SERVICE=worker
export E2B_DEMO_NOW=2026-12-01T00:00:00Z
make up
```

Resume the existing walkthrough at step 19, keeping its database and cursor.
Sending the late October receipt with a December clock would bill it in December
instead of the declared November fixture.

## Fresh assignment data

The walkthrough expects fresh seeded Acme and Cyberdyne accounts, unchanged seed
prices, no prior purchases/invoices, and the literal versions and balances in the
fixture. GUI experimentation can change that state. Follow the complete
[isolated assignment setup](../simulator/billing-scenarios.md#start-an-isolated-assignment-environment)
for project selection, port exports, migrations, service health, CLI execution,
and checkpoints. It uses `e2b-assignment-01`, API port `8183`, portal port `8182`,
and PostgreSQL port `5542` as examples; choose a new name and free ports when
needed. Open the **Billing demo GUI** link printed by `make links` in that same
project. Default port `8082` belongs to development with the default settings.

Choose either the CLI simulator or **Assignment demo** to drive the sequence.
After CLI steps, refresh the account views to inspect stored results. The GUI's
request history contains only requests made by that browser, and its walkthrough
cursor is independent of the CLI checkpoint. Running both drivers against the
same accounts can fail fresh-account checks or response expectations.

Only ended UTC calendar months can close. Before `2026-11-01T00:00:00Z`, the
assignment completes steps 1–12 and stops at October invoice issuance with HTTP
`422`; November closure requires `2026-12-01T00:00:00Z`. The GUI does not override
clocks. The server, not the browser's eligibility hint, decides whether a month
can close. The completed operations remain stored for inspection.

Late usage received after closure can also route according to receipt month;
reproducing the exact fixed November invoice requires the original workflow's
receipt-time assumptions. See [public billing scenario prerequisites](../simulator/billing-scenarios.md#run-the-complete-assignment)
and the [late usage contract](../architecture/accounting-rules.md#utc-months-limits-add-ons-and-late-usage).

Changing project names selects separate volumes; `make down` retains them. Use a
previously unused name for fresh accounts. For missing API names or occupied
ports, follow [simulator connectivity troubleshooting](troubleshooting.md#troubleshoot-simulator-connectivity).

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
money/JSON, request construction, invoice form issue/load workflows, SQL escaping
and scoping, saved retries and
assignment checkpoints. It is not another running service. Browser review should
also cover all screens, actual API replies, Adminer login/prefilled SQL, responsive
layout and invoice printing. Financial browser experiments belong in an isolated
schema or Compose project, not a user's existing demonstration data.

## Delayed billing walkthrough

Select **Delayed platform billing** or **Recover a missed invoice** in Assignment demo. The
[scenario guide](../simulator/delayed-billing.md) lists the clock phases and expected
invoices. Each step displays its required UTC business instant; mismatched startup
metadata blocks sending and retains the cursor. Restart API, worker and portal
with the same `E2B_DEMO_NOW`, preserving their database volume, then reload.

The **Invoices** screen also provides **Recover missed monthly invoices**: enter
a completed through month and keep its operation key for retries. The response
displays each returned snapshot separately in chronological order. A larger gap
than 12 missing months needs consecutive earlier targets; successful retry keys
retain the original range. The ordinary Issue and Load controls keep their own
billing month. SQL inspection includes catch-up command history.

Backend correctness runs separately through [Go E2E scenarios](e2e-testing.md)
in CI, without a browser. Use the [manual backend walkthrough](e2e-walkthrough.md)
for independently inspectable API and worker checkpoints. The existing Node GUI
tests use simulated DOM and mocked responses; interactive GUI requests use real HTTP.
