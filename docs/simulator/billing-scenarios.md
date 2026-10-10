# Public billing scenarios

## Contents

- [Run the complete assignment](#run-the-complete-assignment)
- [Scenarios and review entry points](#scenarios-and-review-entry-points)
- [Durable retries, outages, and restart](#durable-retries-outages-and-restart)
- [Verification](#verification)

The platform simulator runs named HTTP steps with explicit inputs and literal expected
responses. It sends usage increments and financial commands to the real billing API;
it never calculates expected prices, rounding, invoices, or credit. Accounting runs in
the worker. These synthetic sandbox measurements use the assignment's seed catalog.

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

Invoice closure requires a completed UTC calendar month. The real API uses its
server clock, so the fixed October/November 2026 assignment workflow can finish
only from `2026-12-01T00:00:00Z`. Earlier runs stop at an invoice step with HTTP
`422`; usage and financial commands already committed by earlier steps remain.
For review before that date, run `make inbox-test RUN=BillingSimulator`. Those
isolated integration servers declare an invoice clock at `2026-12-01T00:00:00Z`, while
exercising the same month guard, router, worker, and persistence. The price-version
case additionally declares separate insertion times, described below. Requests have no
clock override.

```sh
make up SERVICE=postgres
make migrate
make up SERVICE=api
make up SERVICE=worker
make simulate SCENARIO=billing-assignment STATE=/state/billing-assignment.json
```

The scenario grants Acme USD 25, purchases its recurring add-on, sets Cyberdyne's
USD 15 limit, sends both October measurements, and reads monthly limit status from
the platform. It issues October invoices before delivering late October usage and
November consumption. It then issues November and checks unchanged invoice snapshots.

| Issued invoice | Usage lines, cents | Add-on, cents | Credit line, cents | Total, cents |
| --- | --- | --- | --- | --- |
| `ACME-0001`, October | 1_200 | 2_000 | −1_200 | 2_000 |
| `CYBERDYNE-0001`, October | 617 + 1_200 | — | 0 | 1_817 |
| `ACME-0002`, November | 200 late October + 400 November | 2_000 | −600 | 2_000 |

Acme's final exact balance is 700_000_000 ticks (USD 7). Cyberdyne's October gross
usage is 1_817_283_945 ticks, above its 1_500_000_000-tick limit; November starts at
zero gross usage. Invoice issuance and retries consume no additional credit.

## Scenarios and review entry points

| Scenario JSON | Complete outcome |
| --- | --- |
| [billing-assignment](billing-assignment.json) | All assignment actions, exact October/November invoices and balances, late usage, immutable retry, and platform monthly status reads. |
| [billing-command-retries](billing-command-retries.json) | Matching commands retain original results; changed content conflicts; replaying an older limit preserves the newer limit. |
| [billing-exact-credit](billing-exact-credit.json) | Two 1_250-unit measurements use exactly 10_000 ticks; credit remains 990_000 ticks and the invoice is zero cents. |
| [billing-credit-exhaustion](billing-credit-exhaustion.json) | One cent of credit pays one of two usage cents; the month-end add-on costs 2_000 cents; invoice total is 2_001 cents; a later grant preserves the issued invoice. |
| [billing-limit-status](billing-limit-status.json) | Platform reads below/reached/raised/reset/zero/unlimited statuses; all measured consumption remains billable. |
| [billing-price-versions](billing-price-versions.json) | An 8-cent default and 3-cent customer override produce a 1_100-cent invoice; two new backdated versions return `422` with `effective_from` and leave history unchanged. |
| [billing-price-boundary](billing-price-boundary.json) | A receipt crossing the applicable price boundary becomes a visible processing error; issuance succeeds without billing the quarantined receipt. |
| [billing-month-boundary](billing-month-boundary.json) | A receipt crossing a UTC month boundary becomes a visible processing error; issuance succeeds without billing the quarantined receipt. |

The `billing-price-versions` file uses the automated private-schema runner: its
first two price commands see `2026-10-01T00:00:00Z`, before their October activation
dates, while rejected backdated commands and invoice issuance see
`2026-12-01T00:00:00Z`. A live December API rejects those initial October versions
as backdated. Use `make inbox-test RUN=BillingSimulator` for the fixed scenario;
there is no public clock override. Identical price retries remain valid after
activation. Investigated missing historical prices use [controlled operator SQL](../guides/accounting-recovery.md#controlled-historical-price-repair)
and explicit receipt release, independently of invoice publication.

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

The first command fails at the intentionally lost grant reply, with two completed
read steps. The next process confirms the same grant and finishes all 24 steps.
To exercise service unavailability, stop the API before the first run, run with
`MAX_ATTEMPTS=1`, restart the API, and repeat the same command with the normal retry
budget. Preserve the database, scenario file, source, and state file throughout.

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

`TestBillingSimulatorExecutable` runs these files in independent seeded schemas with
the real HTTP router and worker. Separate named cases cover two `503` replies with
`Retry-After`, an unavailable endpoint followed by a new process, and a lost committed
grant followed by a new process. All financial mutations and observations use public
HTTP APIs. Existing transport tests preserve inbox, barrier, replay, and buffering checks.

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
