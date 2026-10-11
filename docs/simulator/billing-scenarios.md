# Public billing scenarios

## Contents

- [Missed manual invoice](#missed-manual-invoice)
- [Run the complete assignment](#run-the-complete-assignment)
- [Start an isolated assignment environment](#start-an-isolated-assignment-environment)
- [Scenarios and review entry points](#scenarios-and-review-entry-points)
- [Fixed-clock invoice demo](#billing-demo-invoice)
- [Billing assignment](#billing-assignment)
- [Command retries and conflicts](#command-retries-and-conflicts)
- [Exact sub-cent credit](#exact-sub-cent-credit)
- [Zero-total invoices without remainder carry](#zero-total-invoices-without-remainder-carry)
- [Credit exhaustion](#credit-exhaustion)
- [Add-on closing order](#add-on-closing-order)
- [Monthly limit status](#monthly-limit-status)
- [Price versions](#price-versions)
- [Price boundary](#price-boundary)
- [UTC month boundary](#utc-month-boundary)
- [Durable retries, outages, and restart](#durable-retries-outages-and-restart)
- [Verification](#verification)
- [Multi-month platform backlog](#multi-month-platform-backlog)

The platform simulator runs named HTTP steps with explicit inputs and literal expected
responses. It sends usage increments and financial commands to the real billing API;
it never calculates expected prices, rounding, invoices, or credit. Accounting runs in
the worker. These synthetic measurements preserve the assignment's seed catalog. The September backlog scenario explicitly provisions an additional historical price through the public API.

Each JSON step states `name`, `request.method`, `request.path`, and `want.status`.
Requests and expected response fields appear together. Response objects may contain
additional generated fields; array length/order and scalar values must match exactly.
`await: true` on a GET polls an asynchronous result. Unexpected processing errors stop
that wait. `capture` saves the complete response, and `want.same_as` checks the complete
immutable snapshot on a later read or unchanged retry.

## Run the complete assignment

Use a fresh seeded database and a new workflow state file. The first account checks
fail immediately if earlier billing actions have changed either account. Each separate
scenario also requires its own fresh seed accounts; acceptance tests create private
schemas automatically. Changing `SOURCE` isolates receipt identities, not account balances.

Invoice closure requires a completed UTC calendar month. The API uses its configured business clock (real time by default): October closes no earlier than `2026-11-01T00:00:00Z`, and November
no earlier than `2026-12-01T00:00:00Z`. Before the October boundary, the assignment
completes steps 1–12 and stops at step 13 with HTTP `422`; earlier committed
commands remain stored. After the October boundary but before the November
boundary, it cannot issue the November invoice.
The exact November invoice also requires the late October receipt to route to
November. Receipt in December or later routes that charge to a later billing
month, so waiting until December does not by itself reproduce every fixture result.
For a live fixed-clock walkthrough, use the [demo environment](../guides/demo-console.md#fixed-clock-invoice-demo). The short invoice scenario needs one fixed November instant; the complete assignment advances from November to December between steps 18 and 19.
For complete verification independent of the runtime calendar, run
`make inbox-test RUN=BillingSimulator`. Those
isolated integration servers declare an invoice clock at `2026-12-01T00:00:00Z`, and declare November receipt times for late usage, while
exercising the same month guard, router, worker, and persistence. The price-version
case additionally declares separate insertion times, described below. Requests have no
clock override.

### Start an isolated assignment environment

Run these commands from the repository root in the same terminal. The example
requires a previously unused project name and free host ports; choose another
name for a fresh run if this project already contains financial operations.
[Compose projects and ports](../guides/local-development.md#compose-projects-and-ports)
explains how the project selects an independent network, database, and simulator
volume. Export the project once so startup, migrations, GUI, and simulator all
use the same environment:

```sh
export COMPOSE_PROJECT_NAME=e2b-assignment-01
export E2B_API_PORT=8183
export E2B_DOCS_PORT=8182
export E2B_POSTGRES_PORT=5542

make up SERVICE=postgres
make migrate
make up
make ps
make links
```

Stop here if migration or startup fails. Check that PostgreSQL, API, worker, and
portal are healthy before sending requests. Open the printed **Billing demo GUI**
link, `http://127.0.0.1:8182/demo/` with these example ports. The development GUI
on port `8082` shows another project's data when it uses the default settings.
Do not perform independent financial commands on the scenario's accounts.

Run the assignment against this same project:

```sh
make simulate SCENARIO=billing-assignment STATE=/state/billing-assignment.json
```

`make simulate` does not start dependent services. With no project override or
environment selection, plain `make up` uses `e2b-local`; adding
`COMPOSE='docker compose -p e2b-assignment-01'` only to the simulator selects
another network, where its API may be absent. For `lookup api ... no such host`,
follow [simulator connectivity troubleshooting](../guides/troubleshooting.md#troubleshoot-simulator-connectivity).
Host port exports cannot repair that project mismatch.

The CLI prints `Passed step` for each completed step. After the process stops,
inspect its checkpoint without sending requests:

```sh
make simulate SCENARIO=billing-assignment STATE=/state/billing-assignment.json ACTION=status
```

Add `MODE=step` to a run command to complete one observed step per invocation.
Repeat it with the same project, scenario, source, and state file to continue.
Restore the project and port exports in each new terminal. A new state file or
source does not reset account balances, subscriptions, or invoices.

When the runtime calendar prevents October closure, expect `completed=12 steps=24`
and HTTP `422` at step 13. Refresh the GUI for month `2026-10`: Acme has 13 USD
credit and 12 USD gross usage; Cyberdyne has 18.17283945 USD gross usage and has
reached its 15 USD limit. Both accounts have zero pending receipts and processing
errors. These results remain in this project's database; the invoice has not
been issued. Later steps still require the calendar and receipt-time conditions
above.

To verify all 24 steps with the integration runner in the selected project:

```sh
make inbox-test RUN='^TestBillingSimulatorExecutable$/^complete_assignment$'
```

The runner exercises the real simulator, HTTP router, worker, and PostgreSQL in a
private seeded schema with controlled invoice and receipt times. Expect `PASS`.
It removes the private schema after the test; the test's invoices do not appear
in the GUI, and the live scenario's data remains unchanged.

## Scenarios and review entry points

| Step table | Executable JSON | Steps |
| --- | --- | ---: |
| [Missed manual invoice](delayed-billing.md#missed-manual-invoice) | [billing-missed-invoice](billing-missed-invoice.json) | 27 |
| [Billing assignment](#billing-assignment) | [billing-assignment](billing-assignment.json) | 24 |
| [Command retries and conflicts](#command-retries-and-conflicts) | [billing-command-retries](billing-command-retries.json) | 16 |
| [Exact sub-cent credit](#exact-sub-cent-credit) | [billing-exact-credit](billing-exact-credit.json) | 6 |
| [Zero-total invoices without remainder carry](#zero-total-invoices-without-remainder-carry) | [billing-subcent-invoices](billing-subcent-invoices.json) | 15 |
| [Credit exhaustion](#credit-exhaustion) | [billing-credit-exhaustion](billing-credit-exhaustion.json) | 9 |
| [Add-on closing order](#add-on-closing-order) | [billing-addon-order](billing-addon-order.json) | 14 |
| [Monthly limit status](#monthly-limit-status) | [billing-limit-status](billing-limit-status.json) | 15 |
| [Price versions](#price-versions) | [billing-price-versions](billing-price-versions.json) | 8 |
| [Price boundary](#price-boundary) | [billing-price-boundary](billing-price-boundary.json) | 5 |
| [UTC month boundary](#utc-month-boundary) | [billing-month-boundary](billing-month-boundary.json) | 5 |

Each numbered row corresponds to exactly one JSON step. The tables are the
human-readable scenario descriptions; the linked JSON supplies the complete
request and response assertions. All measurements use `cpu_seconds`, schema
version 1, and UTC intervals. Amounts are USD unless labeled as cents or microcents;
1 USD is 100_000_000 microcents. JSON fields ending in `_microcents` contain microcents. Fresh-account reads require zero credit, pending events,
processing errors, and state version. Completed accounting reads require zero
pending events and errors unless the row explicitly expects quarantine.
HTTP `202` means durable receipt; financial processing is checked by later reads.

## billing-demo-invoice

The [nine-step fixture](billing-demo-invoice.json) demonstrates a past-month invoice
against the real API and worker with `E2B_DEMO_NOW=2026-11-01T00:00:00Z`. Follow
[the fresh fixed-clock environment](../guides/demo-console.md#fixed-clock-invoice-demo).
Use fresh Cyberdyne seed state. Existing Acme usage is irrelevant to Cyberdyne's
closure; prior Cyberdyne operations or invoices can invalidate the expectations.

| Step | Input or action | Literal expected result |
| --- | --- | --- |
| 1 | Read Cyberdyne credit. | Balance 0; pending/errors 0; state version 0. |
| 2 | Buy `concurrency_pack` on `2026-10-05`. | HTTP `200`; start month October; monthly price 2_000 cents. |
| 3 | Send four `cpu_seconds` events: sandbox A, October 5, `100_000_000` units; B, October 10, `23_456_789`; A, October 20, `150_000_000`; B, October 25, `50_000_000`. Each interval is 12:00–13:00 UTC. | HTTP `202`; first two use 5 cents per million, last two use 6. |
| 4 | Wait for accounting through credit reads. | Balance 0; pending/errors 0; state version 5. |
| 5 | Read October gross usage. | `1_817_283_945` microcents; unlimited; pending/errors 0. |
| 6 | Issue October invoice, discard successful reply and retry unchanged. | HTTP `200`; `CYBERDYNE-0001`; gross usage `1_817_283_945` microcents; credit 0; total 3_817 cents. |
| 7 | Read October invoice. | Complete snapshot equals step 6. |
| 8 | Repeat October issuance. | Same number, issue time and complete snapshot. |
| 9 | Read final credit state. | Balance 0; pending/errors 0; state version 6. |

The invoice aggregates by price version and usage month, not sandbox. Its lines
are usage **6.17 USD** (`123_456_789` units), usage **12 USD** (`200_000_000`
units), add-on **20 USD**, and credit **0 USD**: total **38.17 USD**. It samples
the configured November issue time while preserving October consumption prices.
No schema migration or seed price changes are required.

## Billing assignment

Source: [billing-assignment.json](billing-assignment.json).
Use the [run prerequisites](#run-the-complete-assignment) for calendar and receipt times.

| Step | Simulator action | Expected result |
| --- | --- | --- |
| 1 | Read Acme's fresh account. | HTTP `200`; zero credit, pending events, errors, and state version. |
| 2 | Read Cyberdyne's fresh account. | HTTP `200`; zero credit, pending events, errors, and state version. |
| 3 | Grant Acme 25 USD with key `welcome`; discard the committed reply and retry unchanged. | HTTP `200`; credit is granted once despite the lost reply. |
| 4 | Purchase Acme's `concurrency_pack` with key `acme-pack` at `2026-10-05`. | HTTP `200`; monthly price 20 USD, start month `2026-10-01`; capture the subscription response. |
| 5 | Set Cyberdyne's monthly limit to 15 USD with key `october-limit`. | HTTP `200`; stored limit is 1_500 cents. |
| 6 | Send the October 10, 12:00–13:00 measurements: Acme `100_000_000` units and Cyberdyne `123_456_789` units. | HTTP `202`; both measurements are durably accepted. |
| 7 | Poll Acme's credit after the first hour. | HTTP `200`; 4 USD consumed, 21 USD remaining (`2_100_000_000` microcents); state version 3. |
| 8 | Poll Cyberdyne's October limit status. | HTTP `200`; gross usage `617_283_945` microcents (6.17283945 USD), limit 15 USD, `limit_reached=false`. |
| 9 | Retry both first-hour measurements with unchanged identities and content. | HTTP `202`; no additional receipt, charge, or credit debit. |
| 10 | Send the October 20, 12:00–13:00 measurements: `200_000_000` units for each customer. | HTTP `202`; two additional measurements are accepted. |
| 11 | Poll Acme's credit after October usage. | HTTP `200`; another 8 USD consumed, 13 USD remaining (`1_300_000_000` microcents); state version 4. |
| 12 | Poll Cyberdyne's October limit status. | HTTP `200`; gross usage `1_817_283_945` microcents (18.17283945 USD), limit 15 USD, `limit_reached=true`. |
| 13 | Issue Acme's October invoice; discard the committed reply and retry unchanged. | HTTP `200`; one `ACME-0001` snapshot: usage 12 USD + add-on 20 USD − credit 12 USD = **20 USD**. Exact gross and used credit are each `1_200_000_000` microcents. |
| 14 | Issue Cyberdyne's October invoice after its limit was reached. | HTTP `200`; `CYBERDYNE-0001`: usage lines 6.17 USD and 12 USD, credit 0, total **18.17 USD**. Exact gross is `1_817_283_945` microcents; all usage remains billable. |
| 15 | Deliver Acme's late October 30, 12:00–13:00 measurement of `50_000_000` units after October closing; discard the committed reply and retry. | HTTP `202`; one receipt, priced using October consumption time; billing month November under the required receipt-time assumptions. |
| 16 | Deliver Acme's November 3, 12:00–13:00 measurement of `100_000_000` units. | HTTP `202`; one additional receipt. |
| 17 | Poll Acme's credit after late October and November usage. | HTTP `200`; 7 USD remaining (`700_000_000` microcents); state version 7. |
| 18 | Poll Cyberdyne's November limit status. | HTTP `200`; gross usage 0, retained limit 15 USD, `limit_reached=false`. |
| 19 | Issue Acme's November invoice. | HTTP `200`; `ACME-0002`: late October usage 2 USD + November usage 4 USD + add-on 20 USD − credit 6 USD = **20 USD**. Exact gross and used credit are each `600_000_000` microcents. |
| 20 | Read Acme's credit after invoice issuance. | HTTP `200`; still 7 USD, state version 8; invoice issuance consumes no additional credit. |
| 21 | Read Acme's October invoice after late usage. | HTTP `200`; complete response equals the snapshot captured at step 13. |
| 22 | Retry Acme's October invoice issuance. | HTTP `200`; the same complete snapshot, number, and issue time; no second invoice. |
| 23 | Read Acme's November invoice. | HTTP `200`; complete response equals the snapshot captured at step 19. |
| 24 | Read Cyberdyne's October invoice. | HTTP `200`; complete response equals the snapshot captured at step 14. |

## Command retries and conflicts

Source: [billing-command-retries.json](billing-command-retries.json).
The default-price activation date `2026-12-01` must not precede the server
UTC date after catalog locking for a new command; unchanged retries retain their original result.

| Step | Simulator action | Expected result |
| --- | --- | --- |
| 1 | Read Acme's fresh account. | HTTP `200`; zero credit, pending events, errors, and state version. |
| 2 | Grant 25 USD with key `welcome`; discard the committed reply and retry unchanged. | HTTP `200`; one grant despite the lost reply. |
| 3 | Repeat the same 25 USD grant and key. | HTTP `200`; original grant returned without another credit increase. |
| 4 | Reuse `welcome` but change the amount to 26 USD. | HTTP `409`, `billing_conflict`; no additional grant. |
| 5 | Poll Acme's credit. | HTTP `200`; exactly 25 USD (`2_500_000_000` microcents), state version 1. |
| 6 | Buy `concurrency_pack` with key `acme-pack` at `2026-10-05T00:00:00Z`. | HTTP `200`; monthly price 20 USD; capture the subscription response. |
| 7 | Retry the identical purchase with `acme-pack`. | HTTP `200`; original purchase returned; no duplicate subscription. |
| 8 | Try the same add-on and purchase time using a new key, `another-pack`. | HTTP `409`, `billing_conflict`; a second subscription is rejected. |
| 9 | Set a 15 USD limit with key `first-limit`. | HTTP `200`; limit 1_500 cents. |
| 10 | Raise the limit to 20 USD with key `raised-limit`. | HTTP `200`; limit 2_000 cents. |
| 11 | Retry the original 15 USD command with `first-limit`. | HTTP `200`; original 1_500-cent response, while the current limit remains 20 USD. |
| 12 | Poll October limit status. | HTTP `200`; limit 20 USD, gross 0, `limit_reached=false`. |
| 13 | Insert a default `cpu_seconds` price of 7 cents per million, effective `2026-12-01`, key `new-default`. | HTTP `200`; capture the new price-version ID. |
| 14 | Retry the identical `new-default` price command. | HTTP `200`; the captured price-version ID and 7-cent price are returned. |
| 15 | Reuse `new-default` but change the price to 8 cents per million. | HTTP `409`, `billing_conflict`; stored price remains unchanged. |
| 16 | Poll Acme's final credit. | HTTP `200`; still 25 USD, state version 4; grants and purchases were not duplicated. |

## Exact sub-cent credit

Source: [billing-exact-credit.json](billing-exact-credit.json).
October must be a completed UTC month before step 5.

| Step | Simulator action | Expected result |
| --- | --- | --- |
| 1 | Read Acme's fresh account. | HTTP `200`; zero credit, pending events, errors, and state version. |
| 2 | Grant 0.01 USD with key `one-cent`. | HTTP `200`; one-cent grant accepted. |
| 3 | Send two `1_250`-unit increments on October 10, 12:00–12:01 and 12:01–12:02. | HTTP `202`; both increments accepted. |
| 4 | Poll Acme's credit after processing. | HTTP `200`; `10_000` microcents consumed; `990_000` microcents (0.0099 USD) remain, state version 3. |
| 5 | Issue the October invoice. | HTTP `200`; `ACME-0001`, total 0 cents; usage `2_500` units, displayed usage and credit lines each 0 cents. Exact gross and used credit are each `10_000` microcents. |
| 6 | Read credit after invoice rounding. | HTTP `200`; still `990_000` microcents, state version 4; no additional microcent consumed. |

## Zero-total invoices without remainder carry

Source: [billing-subcent-invoices.json](billing-subcent-invoices.json).
Use fresh Acme seed data with no credit or add-ons. The fixed business clock must
be at least `2027-01-01T00:00:00Z` to close all three months; integration tests use
that instant. Each receipt contains `50_000` units at the existing Acme override
of 4 cents per million, worth exactly `200_000` microcents (USD 0.002).
Each month's group rounds independently to zero cents. The three exact charges
sum to USD 0.006, but no rounding remainder is carried into a later invoice.
This is the [explicit rounding policy](../architecture/accounting-rules.md#rounding-and-invoice-presentation),
not an additional requirement from the assignment.

| Step | Simulator action | Expected result |
| --- | --- | --- |
| 1 | Read Acme's fresh account. | HTTP `200`; credit, pending events, errors, and state version are all zero. |
| 2 | Send `50_000` October units. | HTTP `202`; accepted. |
| 3 | Wait for October accounting. | HTTP `200`; credit 0, no pending/errors, version 1. |
| 4 | Close October and capture the snapshot. | HTTP `200`; `ACME-0001`, total 0 cents, one usage line with `50_000` units, exact gross `200_000` microcents, credit 0. |
| 5 | Send `50_000` November units. | HTTP `202`; accepted. |
| 6 | Wait for November accounting. | HTTP `200`; credit 0, no pending/errors, version 3. |
| 7 | Close November and capture the snapshot. | HTTP `200`; `ACME-0002`, total 0 cents, one usage line with `50_000` units, exact gross `200_000` microcents, credit 0. |
| 8 | Send `50_000` December units. | HTTP `202`; accepted. |
| 9 | Wait for December accounting. | HTTP `200`; credit 0, no pending/errors, version 5. |
| 10 | Close December and capture the snapshot. | HTTP `200`; `ACME-0003`, total 0 cents, one usage line with `50_000` units, exact gross `200_000` microcents, credit 0. The combined three-month value creates no extra charge. |
| 11–13 | Read each invoice after all closures. | HTTP `200`; each complete response equals its captured snapshot. |
| 14 | Retry October issuance. | HTTP `200`; original October snapshot and number. |
| 15 | Read final credit and state version. | HTTP `200`; credit 0, no pending/errors, version 6; reads and retry caused no financial effects. |

## Credit exhaustion

Source: [billing-credit-exhaustion.json](billing-credit-exhaustion.json).
October must be a completed UTC month before step 6.

| Step | Simulator action | Expected result |
| --- | --- | --- |
| 1 | Read Acme's fresh account. | HTTP `200`; zero credit, pending events, errors, and state version. |
| 2 | Grant 0.01 USD with key `one-cent`. | HTTP `200`; one-cent grant accepted. |
| 3 | Buy `concurrency_pack` with key `acme-pack` at `2026-10-31T23:59:59Z`. | HTTP `200`; full monthly price 20 USD, start month `2026-10-01`, despite purchase in October's last second. |
| 4 | Send `500_000` units for October 10, 12:00–13:00. | HTTP `202`; measurement accepted; gross usage is 0.02 USD when processed. |
| 5 | Poll Acme's credit. | HTTP `200`; credit exhausted to 0 microcents, state version 3. |
| 6 | Issue the October invoice and capture it. | HTTP `200`; `ACME-0001`: usage 0.02 USD + add-on 20 USD − credit 0.01 USD = **20.01 USD**. Exact gross `2_000_000` microcents, used credit `1_000_000` microcents. |
| 7 | Grant another 0.05 USD with key `later`. | HTTP `200`; new grant accepted without rewriting previous allocations. |
| 8 | Read the issued October invoice. | HTTP `200`; complete response equals the snapshot captured at step 6. |
| 9 | Poll Acme's credit. | HTTP `200`; `5_000_000` microcents (0.05 USD), state version 5; no retroactive debit. |

## Add-on closing order

[billing-addon-order.json](billing-addon-order.json) buys the October add-on without
any usage, rejects skipped active months, then issues all three monthly fees.
Run it with a completed December business clock, such as
`E2B_DEMO_NOW=2027-01-01T00:00:00Z`, in a fresh isolated environment. The integration
runner fixes this clock automatically:

```sh
make inbox-test RUN='^TestBillingSimulatorExecutable$/^add-on_alone_preserves_all_monthly_fees$'
```

| Step | Input | Literal expected result |
| --- | --- | --- |
| 1 | Read Acme credit. | `200`; zero credit, pending events and errors; version 0. |
| 2 | Buy `concurrency_pack` on `2026-10-05` with key `addon-order`. | `200`; start month October; monthly price 2_000 cents. |
| 3 | First request December invoice. | `409 billing_conflict`; October and November cannot be skipped. |
| 4–5 | Read October and December invoices. | Both `404 not_found`. |
| 6 | Read credit after rejection. | Zero credit, pending events and errors; version remains 1. |
| 7 | Close October. | `ACME-0001`; one 2_000-cent add-on and zero credit line; total 2_000. Capture complete snapshot. |
| 8 | Request December again. | `409 billing_conflict`; November is required. |
| 9 | Read October. | Complete snapshot matches the capture. |
| 10 | Close November. | `ACME-0002`; add-on 2_000 cents; total 2_000. |
| 11 | Close December. | `ACME-0003`; add-on 2_000 cents; total 2_000. Capture complete snapshot. |
| 12–13 | Retry October and December closing. | Each complete snapshot matches its original; no duplicate fees. |
| 14 | Read credit. | Zero credit, pending events and errors; version 4. |

All invoices have zero gross usage and allocated credit. Together they preserve
6_000 cents in recurring fees. [Add-on closing tests](../../tests/inbox/addon_closing_test.go)
also cover both purchase/closing lock orders, unchanged financial state on rejection,
UTC and Shanghai sessions, future subscriptions, customer isolation and free add-ons.

## Monthly limit status

Source: [billing-limit-status.json](billing-limit-status.json).
October must be a completed UTC month before step 15.

| Step | Simulator action | Expected result |
| --- | --- | --- |
| 1 | Read Cyberdyne's fresh account. | HTTP `200`; zero credit, pending events, errors, and state version. |
| 2 | Set a 15 USD monthly limit with key `first`. | HTTP `200`; limit 1_500 cents. |
| 3 | Send `123_456_789` units for October 10, 12:00–13:00. | HTTP `202`; measurement accepted. |
| 4 | Poll October limit status. | HTTP `200`; gross `617_283_945` microcents (6.17283945 USD), limit 15 USD, `limit_reached=false`. |
| 5 | Send `200_000_000` units for October 20, 12:00–13:00. | HTTP `202`; measurement accepted even though total usage will exceed the limit. |
| 6 | Poll October limit status. | HTTP `200`; gross `1_817_283_945` microcents (18.17283945 USD), limit 15 USD, `limit_reached=true`. |
| 7 | Raise the limit to 20 USD with key `higher`. | HTTP `200`; limit 2_000 cents. |
| 8 | Poll October limit status. | HTTP `200`; unchanged gross `1_817_283_945` microcents, limit 20 USD, `limit_reached=false`. |
| 9 | Restore the 15 USD limit with key `restore`. | HTTP `200`; limit 1_500 cents. |
| 10 | Read November limit status. | HTTP `200`; gross 0, retained limit 15 USD, `limit_reached=false`. |
| 11 | Set the limit to 0 with key `zero`. | HTTP `200`; zero is stored as a real limit. |
| 12 | Read November status with zero spend and a zero limit. | HTTP `200`; gross 0, limit 0, `limit_reached=true`. |
| 13 | Remove the limit with key `unlimited` and `limit_cents=null`. | HTTP `200`; unlimited configuration stored. |
| 14 | Read October status with the limit removed. | HTTP `200`; gross remains `1_817_283_945` microcents, limit NULL, `limit_reached=false`. |
| 15 | Issue Cyberdyne's October invoice. | HTTP `200`; `CYBERDYNE-0001`, usage lines 6.17 USD and 12 USD, credit 0, total **18.17 USD**; exceeding the limit discarded no usage. |

## Price versions

Source: [billing-price-versions.json](billing-price-versions.json).
Use the private-schema runner for this fixed scenario. Steps 2–3 use insertion
time `2026-10-01T00:00:00Z`; steps 6–8 use `2026-12-01T00:00:00Z`.
A live December API rejects steps 2–3 as backdated; HTTP has no clock override.
Historical catalog repair uses [controlled operator SQL](../guides/accounting-recovery.md#controlled-historical-price-repair).

| Step | Simulator action | Expected result |
| --- | --- | --- |
| 1 | Read Cyberdyne's fresh account. | HTTP `200`; zero credit, pending events, errors, and state version. |
| 2 | Insert a default price of 8 cents per million, effective `2026-10-12`, key `new-default`. | HTTP `200`; price scheduled before activation; capture the new price-version ID. |
| 3 | Insert Cyberdyne's override of 3 cents per million, effective `2026-10-19`, key `new-override`. | HTTP `200`; capture the customer price-version ID. |
| 4 | Send Cyberdyne `100_000_000` units for each hour: October 13, 12:00–13:00 and October 20, 12:00–13:00. | HTTP `202`; two receipts accepted for the new default and customer override respectively. |
| 5 | Poll Cyberdyne's credit after rating. | HTTP `200`; credit 0, state version 2; both receipts processed without errors. |
| 6 | Try a new default of 9 cents per million, effective `2026-10-12`, key `retroactive-default`. | HTTP `422`, `invalid_command`, field `effective_from`; rated history remains unchanged. |
| 7 | Try a new Cyberdyne override of 1 cent per million, effective `2026-10-10`, key `retroactive-override`. | HTTP `422`, `invalid_command`, field `effective_from`; rated history remains unchanged. |
| 8 | Issue Cyberdyne's October invoice. | HTTP `200`; `CYBERDYNE-0001`, usage 8 USD and 3 USD, credit 0, total **11 USD**. Exact gross `1_100_000_000` microcents; line IDs match the captured prices, ordered by price-version ID. |

## Price boundary

Source: [billing-price-boundary.json](billing-price-boundary.json).
October must be a completed UTC month before step 4.

| Step | Simulator action | Expected result |
| --- | --- | --- |
| 1 | Read Cyberdyne's fresh account. | HTTP `200`; zero credit, pending events, errors, and state version. |
| 2 | Send 100 units spanning `2026-10-14T23:30:00Z`–`2026-10-15T00:30:00Z`, across the seed price change. | HTTP `202`; the unsupported interval is durably accepted into the inbox. |
| 3 | Poll Cyberdyne's credit after worker inspection. | HTTP `200`; credit 0, pending 0, processing errors 1, state version 0; quarantine creates no financial charge or credit effect. |
| 4 | Issue Cyberdyne's October invoice with the receipt quarantined. | HTTP `200`; `CYBERDYNE-0001`, total 0, exact gross and used credit 0; only a zero-valued credit line. Quarantine does not block issuance. |
| 5 | Read Cyberdyne's October invoice. | HTTP `200`; `CYBERDYNE-0001` remains readable with total 0. |

## UTC month boundary

Source: [billing-month-boundary.json](billing-month-boundary.json).
October must be a completed UTC month before step 4.

| Step | Simulator action | Expected result |
| --- | --- | --- |
| 1 | Read Acme's fresh account. | HTTP `200`; zero credit, pending events, errors, and state version. |
| 2 | Send 100 units spanning `2026-10-31T23:30:00Z`–`2026-11-01T00:30:00Z`, across the UTC month boundary. | HTTP `202`; the unsupported interval is durably accepted into the inbox. |
| 3 | Poll Acme's credit after worker inspection. | HTTP `200`; credit 0, pending 0, processing errors 1, state version 0; quarantine creates no financial charge or credit effect. |
| 4 | Issue Acme's October invoice with the receipt quarantined. | HTTP `200`; `ACME-0001`, total 0, exact gross and used credit 0; only a zero-valued credit line. Quarantine does not block issuance. |
| 5 | Read Acme's October invoice. | HTTP `200`; `ACME-0001` remains readable with total 0. |

Financial command fixtures use the public calendar-date contract:
`effective_from` and `purchased_at` contain `YYYY-MM-DD`, resolved by the backend
to midnight UTC. A new price date must be today or later after catalog locking;
today remains subject to rated-history protection. Add-on purchases can use open
historical months or future dates, while closing and closed months conflict.
Credit grants supply an amount and operation key; the backend determines their
ledger audit time. Requests and responses contain no `recorded_at`.

## Durable retries, outages, and restart

`fault.lose_response: true` discards one successful response **after the real API has
committed**. The lost-response marker is checkpointed before retry; the next attempt
uses the same operation identity and content. The assignment does this for a grant,
invoice issuance, and late usage. `LOSE_RESPONSE=1` injects one additional loss on the
first successful POST in a saved run.

To observe recovery in separate processes, use a fresh assignment database:

```sh
make simulate SCENARIO=billing-assignment STATE=/state/billing-assignment.json MAX_ATTEMPTS=1
make simulate SCENARIO=billing-assignment STATE=/state/billing-assignment.json
make simulate SCENARIO=billing-assignment STATE=/state/billing-assignment.json ACTION=status
```

Preserve the database, scenario file, source, and state file throughout recovery.
Completing all 24 assignment steps requires the same calendar and receipt-time
prerequisites as the [assignment table](#billing-assignment).

### Lost committed grant and a new producer process

| Step | Simulator action | Expected result |
| --- | --- | --- |
| 1 | Start the fresh assignment with `MAX_ATTEMPTS=1`. | Process fails at the injected lost grant reply; `completed=2 steps=24`, but the 25 USD grant has committed once. |
| 2 | Start a new process with the same scenario, source, and state file, using the normal retry budget. | Original grant is confirmed without another increase; execution continues from step 3 and completes 24 steps when prerequisites hold. |
| 3 | Read progress using `ACTION=status`, then run the completed workflow again. | `completed=24 steps=24`; status performs no HTTP request, and both commands preserve the completed checkpoint. |

### HTTP outage and Retry-After

Automated entry point: `TestBillingSimulatorExecutable/HTTP_outage_recovers_without_financial_drift`.

| Step | Simulator action | Expected result |
| --- | --- | --- |
| 1 | Start the assignment through a test gateway that rejects its first two requests. | Two HTTP `503` responses with `Retry-After: 1`; neither request executes a financial command. |
| 2 | Retry the current step, honoring the delay, after the gateway becomes available. | The [24 assignment steps](#billing-assignment) pass with their original financial results and unchanged command identities. |

### Unavailable API and a new producer process

Automated entry point: `TestBillingSimulatorExecutable/unavailable_API_retains_the_plan_for_a_new_process`.
The automated case uses [exact sub-cent credit](#exact-sub-cent-credit);
the same recovery controls can be applied to the assignment.

| Step | Simulator action | Expected result |
| --- | --- | --- |
| 1 | Start `billing-exact-credit` against an unavailable endpoint with `MAX_ATTEMPTS=1`. | Process fails before its first successful read; completed steps 0; the original plan and failed-step error remain saved. |
| 2 | Start a new process with the original plan, source, and state file against the available API. | All 6 steps pass; final credit `990_000` microcents, zero-cent invoice, no duplicated effects. |

Network failures and unexpected HTTP `408`, `429`, or `5xx` responses retry with
exponential backoff. `Retry-After` is a minimum delay. Other unexpected HTTP results
or response assertions stop the workflow. `MAX_ATTEMPTS` bounds each step; zero
retries until interrupted. The state file retains the failing step and error.
`MODE=step` executes one observed step per command; `ACTION=send` resumes like `run`.
`status` prints progress without HTTP or changing the state file.

An exclusive state lock prevents concurrent producers. Checkpoints use a temporary
file, fsync, atomic rename, and directory fsync. A crash before checkpoint confirmation
repeats the public operation. A changed scenario or source is rejected; keep the
original file for recovery. The guarantee assumes retained local storage. This
simulator injects transport failures; database corruption and disk loss are outside it.

Billing workflows declare fixed measurement inputs. Transport generation options
such as `SANDBOXES`, `INTERVAL`, `DUPLICATES`, `REVERSE`, and `ADVANCE` belong to the
legacy transport scenarios and are rejected when changed for a billing workflow.

## Verification

```sh
make test RUN=BillingSimulator
make test RUN=Simulator
make test
```

`TestBillingSimulatorExecutable` runs the step tables above in independent seeded
schemas with the real HTTP router and worker. All financial mutations and observations
use public HTTP APIs. The [transport tables](transport-scenarios.md) cover the
separate usage-only executable scenarios.

## Generated resource IDs and request keys

Price, add-on, credit, and spend-limit requests declare
`request.body.idempotency_key`. The sender transmits the original JSON body
unchanged across retries and checkpoint resumes. No key header is generated.
Existing checkpoints with the former request layout need a new state file; the
plan comparison prevents silently resuming a changed request. Request
bodies omit resource IDs. Creation responses can be captured; a later expectation
uses `{{capture.<name>.price_version_id}}` or
`{{capture.<name>.subscription_id}}` to verify references to the generated resource.
Financial inputs and expected amounts remain literal. The price-version scenario
sets `want.usage_line_order` to `price_version_id` for its single-month, single-metric
invoice: it orders only the expected leading usage lines by the captured IDs, then
still checks complete array order and every amount. Other expectations keep their
original array order.

## Multi-month platform backlog

The [delayed billing guide](delayed-billing.md) documents the
[billing-delayed-platform fixture](billing-delayed-platform.json), including its
trusted clock phases, cancellation policy, all five invoice calculations and
related worker-delay regressions. It is selectable in the GUI's Assignment demo.

## Missed manual invoice

The [recovery timeline](delayed-billing.md#missed-manual-invoice) documents
[billing-missed-invoice.json](billing-missed-invoice.json): a normal October invoice,
a forgotten November, and atomic catch-up through December. The GUI offers the
same 27-step plan and a manual recovery form. Expected monthly totals are USD 20,
USD 25, USD 32 and USD 25, including late receipts and recurring add-on fees.
