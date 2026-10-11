# Platform transport simulator

The separate Go executable [cmd/platform-simulator](../../cmd/platform-simulator/main.go)
creates synthetic measurement increments and sends them through the actual
`POST /usage/batches` interface. It supplies no prices or monetary charges.
The named `simulator_data` volume retains the plan, stable identities, release
cursor, and delivery receipts across commands and container restarts.

Select the same [Compose project](../guides/local-development.md#compose-projects-and-ports)
for startup, migrations, and every simulator command. `make simulate` does not
start the API or worker. If you prepared an isolated environment, retain its
`COMPOSE_PROJECT_NAME` and port exports in this terminal; restore them in a new
terminal. `lookup api ... no such host` can indicate that the API is absent from
the selected project; see [connectivity troubleshooting](../guides/troubleshooting.md#troubleshoot-simulator-connectivity).

Start the API after [applying migrations](../guides/local-development.md#initialize-the-database):

```sh
make up SERVICE=api
make simulate SCENARIO=assignment MODE=step
make simulate SCENARIO=assignment MODE=step
make simulate ACTION=status
```

Each `MODE=step` invocation releases at most one step. `MODE=fast` (the default)
releases the current phase at once, stopping at the same operator barrier.
No command waits for the real calendar to reach fixture dates, and faster
delivery never changes consumption times. Invoice closure separately requires
the complete UTC month to have ended.

After October accounting and invoice issuance, explicitly release late usage:

```sh
make simulate ADVANCE=1 MODE=step
make simulate MODE=step
```

`make simulate ADVANCE=1 MODE=fast` releases both remaining steps together. The barrier records
operator intent; the simulator cannot check invoice or accounting completion.
The legacy transport scenario uses an operator barrier. The separate
[public billing scenarios](billing-scenarios.md) automate accounting,
credit, add-on purchases, invoice issuance, and platform monthly-status reads through
the documented financial APIs. HTTP `202`
confirms the whole batch's durable inbox receipt, not financial processing.

For financial commands and invoice assertions, use the
[billing assignment step table and prerequisites](billing-scenarios.md#billing-assignment)
with fresh seeded accounts, API, and worker.

Use the [complete isolated setup](billing-scenarios.md#start-an-isolated-assignment-environment)
when the selected project's accounts already contain experiments. After setup,
run this command with that same project selection:

```sh
make simulate SCENARIO=billing-assignment STATE=/state/billing-assignment.json
```

## Scenario step tables

The tables below are the human-readable transport scenario descriptions. They
use one sandbox per customer and one-hour assignment intervals unless a row
states otherwise. All measurements use `cpu_seconds`, schema version 1, and
UTC timestamps. Receipt counts apply only to the scenario's source namespace.
Use a separate state file and source for each independent run; independent
financial experiments also require a fresh database. Keep the same Compose
project for startup, migrations, API, worker, and simulator commands.

### Assignment transport

Source: [assignmentSteps](../../internal/simulator/scenario.go).
Commands use `SCENARIO=assignment MODE=step` and the same source and state file.

| Step | Simulator action | Expected result |
| --- | --- | --- |
| 1 | Release October 10, 2026, 12:00–13:00: Acme `100_000_000` units, Cyberdyne `123_456_789` units. | HTTP `202`; 2 durable receipts. No financial command is sent. |
| 2 | Release October 20, 2026, 12:00–13:00: `200_000_000` units for each customer. | HTTP `202`; 4 receipts in total. |
| 3 | After manually verifying October accounting and issuing October invoices, pass the barrier with `ADVANCE=1`; release Acme's October 30, 12:00–13:00, `50_000_000` units. | HTTP `202`; 5 receipts in total. Without `ADVANCE=1`, the simulator reports `Waiting at barrier` and retains 4 receipts. |
| 4 | Release Acme's November 3, 12:00–13:00, `100_000_000` units. | HTTP `202`; 6 receipts in total. |

### Lost acknowledgement, restart, assignment barrier, and replay

Automated entry point:
`TestSimulatorExecutable/lost_acknowledgement,_restart,_assignment_barrier,_and_replay`.
Source: [transport executable tests](../../tests/api/simulator_test.go).
Use the same source and state file for all rows.

| Step | Simulator action | Expected result |
| --- | --- | --- |
| 1 | Run `SCENARIO=lost-response MODE=step MAX_ATTEMPTS=1`: October 10, 12:00–13:00, Acme `100_000_000` units and Cyberdyne `123_456_789` units. | First HTTP `202` is discarded after commit; process fails with `generated=2 pending=2 delivered=0`, while the inbox already contains 2 receipts. |
| 2 | Start a new process with `ACTION=send BATCH_SIZE=1`. | Both original measurements are confirmed; process succeeds, receipt count remains 2, and stored content and receipt metadata remain unchanged. |
| 3 | Run `MODE=fast`: October 20, 12:00–13:00, `200_000_000` units for each customer. | Second-hour measurements are accepted; process succeeds, receipt count 4. |
| 4 | Run `MODE=fast` again without advancing the barrier. | Process succeeds with `Waiting at barrier`; receipt count 4, inbox unchanged. |
| 5 | Run `ADVANCE=1 MODE=fast` after the operator's October-closing check: Acme `50_000_000` units for October 30, 12:00–13:00, and `100_000_000` units for November 3, 12:00–13:00. | Late October and November measurements are accepted; receipt count 6. |
| 6 | Replay with `ACTION=replay SCENARIO=duplicates BATCH_SIZE=1 REVERSE=1`. | Reversed delivery and extra identical copies succeed; receipt count stays 6, with the complete inbox snapshot unchanged. |

### Duplicate delivery

Commands use `SCENARIO=duplicates MODE=step` with a fresh state file and source.
Every batch has one extra identical copy. The assignment's operator barrier still applies.

| Step | Simulator action | Expected result |
| --- | --- | --- |
| 1 | Send October 10, 12:00–13:00 and its extra copy: Acme `100_000_000` units, Cyberdyne `123_456_789` units. | Both sends receive HTTP `202`; only 2 receipts are stored. |
| 2 | Send October 20, 12:00–13:00 and its extra copy: `200_000_000` units for each customer. | Both sends receive HTTP `202`; only 4 receipts in total. |
| 3 | Pass the operator barrier with `ADVANCE=1`; send Acme's October 30, 12:00–13:00, `50_000_000` units and their extra copy. | HTTP `202`; only 5 receipts in total. |
| 4 | Send Acme's November 3, 12:00–13:00, `100_000_000` units and their extra copy. | HTTP `202`; only 6 receipts in total; duplicate delivery adds no consumption. |

### Offline generation survives a failed delivery process

Automated entry point:
`TestSimulatorExecutable/offline_generation_survives_a_failed_delivery_process`.
Source: [transport executable tests](../../tests/api/simulator_test.go).
Use a fresh state file and source, retaining them across all four processes.

| Step | Simulator action | Expected result |
| --- | --- | --- |
| 1 | Run `ACTION=generate MODE=step` for October 10, 12:00–13:00: Acme `100_000_000` and Cyberdyne `123_456_789` units. | Process succeeds without HTTP; both measurements are saved locally; inbox count 0. |
| 2 | Run `ACTION=send MAX_ATTEMPTS=1` against an unavailable API endpoint. | Process fails with `pending=2 delivered=0`; inbox count remains 0 and the original buffer survives. |
| 3 | Start a new process with `ACTION=send` against the available API. | Process succeeds; 2 receipts stored with the original identities and measurements. |
| 4 | Run `ACTION=send` again. | Process succeeds without resending a confirmed buffer; receipt count remains 2, inbox snapshot unchanged. |

### API downtime during delivery

Use a fresh assignment state file and source. Run the API restart from another terminal.

| Step | Simulator action | Expected result |
| --- | --- | --- |
| 1 | Stop the API with `make stop SERVICE=api`, then run `make simulate MODE=step`: October 10, 12:00–13:00, Acme `100_000_000` units and Cyberdyne `123_456_789` units. | Both measurements remain pending; connection attempts fail and retry, with no acknowledgement recorded. |
| 2 | Start the same project's API using `make up SERVICE=api`. | The running simulator retries unchanged measurements and receives HTTP `202`; 2 receipts stored. |
| 3 | Read progress using `make simulate ACTION=status`. | Generated 2, pending 0, delivered 2; no future assignment phase has been released. |

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

The [lost-response](#lost-acknowledgement-restart-assignment-barrier-and-replay)
and [duplicate-delivery](#duplicate-delivery) tables define the fault cases.
The [API downtime table](#api-downtime-during-delivery) uses these commands:

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

Edit the tracked [custom scenario example](custom-scenario.json)
or add a local JSON file in the same directory. Each step has a unique `name`,
an optional operator `barrier`, and an `events` array with every measurement
field explicitly supplied except `source`, which comes from `SOURCE`.
Unknown fields, null or missing event values, invalid measurements, and repeated
event identities are rejected before delivery. Explicit zero units are valid.

```sh
make simulate SCENARIO=custom SOURCE=custom-example STATE=/state/custom.json MODE=step
make simulate SCENARIO=custom SOURCE=custom-example STATE=/state/custom.json ADVANCE=1
```

### Custom example

Source: [custom-scenario.json](custom-scenario.json).
Use the commands above with a new state file and an unused source.

| Step | Simulator action | Expected result |
| --- | --- | --- |
| 1 | Release `first-minute`: Acme sandbox `acme-sandbox-001`, event `custom-acme-minute-001`, October 10, 2026, 12:00–12:01, 60 units. | HTTP `202`; one receipt stored. |
| 2 | Attempt to continue without `ADVANCE=1`. | `Waiting at barrier`; the second minute remains unreleased and receipt count stays 1. |
| 3 | Pass the barrier with `ADVANCE=1`; release `second-minute`, event `custom-acme-minute-002`, 12:01–12:02, explicitly 0 units. | HTTP `202`; a valid zero-unit receipt is stored, for 2 receipts in total. |

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
