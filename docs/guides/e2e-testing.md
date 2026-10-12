# Backend E2E scenarios

The E2E suite verifies billing-api, the accounting worker and PostgreSQL as
separate Compose services. Requests travel over real HTTP, the real worker
commits accounting, and an independent database connection checks persisted
results. No browser or GUI is involved. The simulator is an independent producer
executable; its JSON fixtures contain public inputs and literal expected outputs.

Use this page to understand the scenarios and their pass conditions. Use the
[manual walkthrough](e2e-walkthrough.md) to execute individual steps and inspect
intermediate results. The longer [acceptance procedure](final-acceptance-testing.md)
retains detailed SQL, invoice tables and visual presentation instructions.

## Run automatically

Requirements are Docker, Compose, Make and Bash. A local Go installation is not
required. Run from the repository root:

```sh
make e2e-test
# Equivalent suite selection:
make test SUITE=e2e
# One complete scenario, including all its prerequisite steps:
make e2e-test RUN='^TestBillingE2E$/^assignment_survives_API_outage_worker_pause_and_service_restart$'
```

Unfiltered `make test` also includes E2E. `SUITE=go` keeps the unit and integration
suites separate; a `RUN` filter defaults to that suite, so use `make e2e-test` or
explicit `SUITE=e2e` for E2E filters. Select a complete scenario rather than one
nested step: later steps depend on the earlier state.

The [test controller](../../tests/e2e/billing_test.go) creates a fresh generated
`e2b-e2e-*` project with automatically assigned loopback host ports. It does not
use the development project, `.env`, passwords or volumes. Within that project,
each sequential scenario stops API/worker and rebuilds the owned database schema
through the regular transactional migration runner. Its producer checkpoint is
separate from every other scenario. Do not run these stateful scenarios in parallel
inside one project; independent invocations have separate projects.

The runner's test-only image accesses the local Docker Unix socket to stop,
restart and recreate the services. Normal application images retain their
unprivileged runtime. Remote Docker daemons are not supported by this launcher.
The controller needs the same absolute repository path inside and outside its
container so Compose bind mounts refer to the host checkout.

## Calendar and completion boundaries

API and worker use the same `E2B_DEMO_NOW` value. Before changing it, the controller
waits for ordinary pending usage_inbox rows to finish, captures logs and gracefully stops
both services. It then recreates them with the new timestamp, keeping PostgreSQL
and the producer checkpoint. No HTTP parameter changes the clock.

Business time drives usage_inbox rows, price validation, financial audit records,
current-month reads and publication. HTTP deadlines, polling, retry, heartbeat,
shutdown, migration audit time and service log timestamps keep real time.
Changing the business clock does not rewrite an existing usage_inbox row or invoice.

The assignment receives late October usage during November and publishes the
November invoice in December. The price-version scenario creates its versions
before activation, waits for rating, and only then tests rejected backdated prices.
Timeline fixtures declare `business_time` next to every public step. Other clock
boundaries are named explicitly in the E2E scenario declarations.

A calendar jump does not produce historical events or run a scheduler. Monthly
issuance and catch-up remain explicit API operations. Asynchronous checks poll
expected state with bounded real deadlines, rather than waiting a guessed duration.

## Acceptance procedure coverage

Each row describes a backend pass condition, rather than just a successful HTTP
status. GUI layout, browser requests in Scalar and human architecture review remain
presentation/review activities outside backend E2E.

| Scenario under `TestBillingE2E` | Inputs and actions | Required result | Manual counterpart |
| --- | --- | --- | --- |
| `fresh_and_concurrent_migrations_preserve_seeds` | Two fresh runners, repeated and concurrent reruns, PostgreSQL restart | Versions 1–13 exactly once; complete migration timestamps and literal customers, addresses, metric, prices, add-on and empty accounts preserved | Acceptance 2.2, 2.4, 3.4 |
| `assignment_survives_API_outage_worker_pause_and_service_restart` | All 24 assignment steps; stopped API; lost committed grant/invoice/usage replies; stopped worker; usage_inbox row replay; November/December transitions | Failed delivery advances no checkpoint; one grant; two durable unprocessed usage_inbox rows before worker restart; six usage_inbox rows/rating links; five groups; Acme invoices 2_000/2_000 cents and Cyberdyne 1_817 cents; Acme balance 700_000_000 microcents; complete SQL and HTTP snapshots unchanged after restart/retries | Acceptance 4–7 |
| `usage_conflicts_and_concurrent_retries_recover_once` | Original usage_inbox row, batch with new + changed usage_inbox row, batch with new + invalid usage_inbox row, eight concurrent retries, worker restart | HTTP 409/422 leave all rows unchanged; only one usage_inbox row is pending and later rated; gross 400_000_000 microcents for 100_000_000 units, no credit entries | HTTP conflict/retry checks and worker recovery |
| `credit_covered_gross_limit_excludes_add-on` | USD 25 credit, USD 20 add-on, USD 15 limit, 400_000_000 units, invoice, USD 20 raised limit | Gross USD 16 reaches the limit despite credit; add-on excluded; October invoice USD 20; USD 9 credit remains; raising limit clears signal; current UTC month resets gross | Acceptance 6.3 and 8 |
| `six_month_clock_jump_catches_up_recurring_invoices` | Buy in October; reject March closing at its last microsecond; jump to exact April boundary; retry purchase and catch-up | Rejection changes nothing; six consecutive full USD 20 fees, total 12_000 cents, numbering 1–6; no usage or ledger entries; original October command timestamp preserved | [Six-month manual case](e2e-walkthrough.md#six-month-clock-jump) |

## Shared public workflow coverage

Every step of every published billing JSON fixture also runs against the separate
service containers. The assignment above uses `billing-assignment.json`; the
remaining fixtures are listed below. Read the linked catalog for their complete
step-by-step request/response tables.

| E2E scenario | Public fixture / explanation | Expected behavior |
| --- | --- | --- |
| `fixed_clock_invoice` | [Invoice demo](../simulator/billing-scenarios.md#billing-demo-invoice) | Four measurements, two prices and full add-on; complete invoice reads/retries are identical |
| `command_retries_and_conflicts` | [Command retries](../simulator/billing-scenarios.md#command-retries-and-conflicts) | One grant/purchase/price effect; changed commands conflict; old limit retry cannot revert a newer limit |
| `exact_subcent_credit` | [Exact credit](../simulator/billing-scenarios.md#exact-sub-cent-credit) | Fractional microcents remain exact through accounting and zero-cent presentation |
| `zero_cent_invoices_without_remainder_carry` | [Subcent invoices](../simulator/billing-scenarios.md#zero-total-invoices-without-remainder-carry) | Three USD 0.002 charges yield three zero-total invoices with retained usage and no carry |
| `credit_exhaustion_preserves_full_add-on_fee` | [Exhaustion](../simulator/billing-scenarios.md#credit-exhaustion) | Partial credit is exhausted once; full recurring fee remains payable; later grant cannot rewrite the invoice |
| `every_recurring_add-on_month_is_retained` | [Add-on order](../simulator/billing-scenarios.md#add-on-closing-order) | Skipped active months rejected without writes; each of three months retains one full fee |
| `monthly_limit_configuration_and_reset` | [Limits](../simulator/billing-scenarios.md#monthly-limit-status) | Below/reached/raised/zero/unlimited states; new month zero gross; usage beyond the limit remains billed |
| `historical_prices_and_customer_override` | [Price versions](../simulator/billing-scenarios.md#price-versions) | Consumption selects the eligible default/override; rated history cannot be changed by backdating |
| `price_boundary_quarantine_permits_closing` | [Price boundary](../simulator/billing-scenarios.md#price-boundary) | Crossing interval is quarantined with no charge/credit effect; closing remains possible |
| `UTC_month_boundary_quarantine_permits_closing` | [Month boundary](../simulator/billing-scenarios.md#utc-month-boundary) | Same isolation for an unsupported cross-month interval |
| `multi_month_delayed_platform_billing` | [Platform backlog](../simulator/delayed-billing.md#platform-backlog-across-septemberjanuary) | All 35 steps; original-month gross and historical prices; immutable five invoices, totaling USD 407; cancellation/reactivation and late replay preserved |
| `missed_manual_invoices_recover_atomically` | [Missed invoices](../simulator/delayed-billing.md#missed-manual-invoice) | All 27 steps; atomic two-month catch-up with retained retry range; late usage and every original snapshot preserved |

For every lost-reply fixture step, the first producer deliberately discards the
successful reply and exits with an unchanged cursor. A new executable resumes it;
complete financial rows must remain unchanged. This is controlled reply loss in
the client, not a claim of TCP failure. The existing integration suite additionally
covers connection loss after commit and producer death at the commit/checkpoint
boundary; both suites remain in CI.

All public workflows independently check ledger/balance conservation, one rating
per completed usage_inbox row, zero ratings for quarantined usage_inbox rows, credit within gross,
and invoice line sums. Literal invoice expectations are never calculated by the
production accounting code.

## CI and evidence

[CI](../../.github/workflows/ci.yml) runs the `Backend E2E` job on push, pull request
and merge queue, alongside the existing `All tests` job. Both must pass before
merging. The E2E job uploads `backend-e2e-evidence` even on failure.

The command prints its evidence path under ignored `tools/e2e-results/`. Keep:

- `tests.log`: named scenario/step results and failure messages.
- Scenario `compose.log`, `producer.log`, `http.log`, `database.log`: actual commands,
  inputs, responses and SQL observations when used by that scenario.
- Producer `*-state.json`, `final-state.json` and restart/seed snapshots.
- `services-*.log`: each service generation before clock changes, restart or reset.
- Top-level service inventory, final logs and `cleanup.log`.

Normal success or failure removes only the generated project and volumes after
saving evidence. A killed host/CI runner may prevent the shell trap from running;
use the printed project name to inspect and remove that invocation's resources.
The suite does not establish disk-loss recovery, database-host failover or capacity.
