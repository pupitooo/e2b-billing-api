# Platform transport simulator

The separate Go executable [cmd/platform-simulator](../../cmd/platform-simulator/main.go)
creates synthetic measurement increments and sends them through the actual
`POST /usage/batches` interface. It supplies no prices or monetary charges.
The named `simulator_data` volume retains the plan, stable identities, release
cursor, and delivery receipts across commands and container restarts.

Start the API after [applying migrations](../guides/local-development.md#initialize-the-database):

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
Invoice closure separately requires the complete UTC month to have ended.

After October accounting and invoice issuance, explicitly release late usage:

```sh
make simulate ADVANCE=1 MODE=step
make simulate MODE=step
```

These commands deliver Acme's October 30 measurement and then November 3.
`make simulate ADVANCE=1 MODE=fast` releases both together. The barrier records
operator intent; the simulator cannot check invoice or accounting completion.
The legacy transport scenario uses an operator barrier. The separate
[public billing scenarios](../../docs/simulator/billing-scenarios.md) automate accounting,
credit, add-on purchases, invoice issuance, and platform monthly-status reads through
the documented financial APIs. HTTP `202`
confirms the whole batch's durable inbox receipt, not financial processing.

Run the complete billing assignment against fresh seeded accounts with the API and
worker running. Its fixed October/November 2026 invoices require the server clock
to have reached `2026-12-01T00:00:00Z`; private-schema integration tests use an
explicit clock and can verify the full workflow earlier:

```sh
make simulate SCENARIO=billing-assignment STATE=/state/billing-assignment.json
```

Its named JSON steps declare requests and literal expected responses together,
including exact invoice lines, credit ticks, and faults after committed replies.
The [billing scenario guide](../../docs/simulator/billing-scenarios.md) lists the remaining
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

Edit the tracked [custom scenario example](../../docs/simulator/custom-scenario.json)
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
