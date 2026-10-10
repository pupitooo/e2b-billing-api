# Final acceptance test procedure

## Contents

- [1. Scope and acceptance decisions](#1-scope-and-acceptance-decisions)
- [2. Automated regression gate in an isolated project](#2-automated-regression-gate-in-an-isolated-project)
- [3. Fresh assignment environment and seed inspection](#3-fresh-assignment-environment-and-seed-inspection)
- [4. Walk through the complete assignment and transport failures](#4-walk-through-the-complete-assignment-and-transport-failures)
- [5. Final invoices and independently persisted state](#5-final-invoices-and-independently-persisted-state)
- [6. Remaining business, integrity, and concurrency scenarios](#6-remaining-business-integrity-and-concurrency-scenarios)
- [7. Persistence and immutable reads across restart](#7-persistence-and-immutable-reads-across-restart)
- [8. Credit-covered gross spend and add-on exclusion](#8-credit-covered-gross-spend-and-add-on-exclusion)
- [9. Architecture review, limitations, and completion record](#9-architecture-review-limitations-and-completion-record)

Use this procedure to review one revision of the complete billing service. Run the
automated regression gate, walk through the assignment using the public API, and
compare its responses with persisted accounting state. Record evidence before
declaring a requirement passed. A successful HTTP request alone is insufficient.

All monetary expectations below use the assignment's USD seed catalog. Calendar
months are UTC. One cent is 1_000_000 ticks; invoice amounts are integer cents and
exact API amounts are decimal strings. Underscores in prose and tables group
digits; copyable JSON and SQL retain their valid numeric syntax.

New invoices can close only completed UTC calendar months. The manual walkthrough
and live simulator use fixed October/November 2026 inputs and therefore require
the server clock to have reached `2026-12-01T00:00:00Z`. Before that date, review
their complete results through the private-schema automated tests, whose explicit
server clocks are independent of the current date. A premature manual invoice
request returns `422` without financial changes; earlier workflow commands may
already have committed. HTTP requests cannot override the server clock.

## 1. Scope and acceptance decisions

The binding example has two customers, `cpu_seconds`, default prices of USD 0.05
and USD 0.06 per million units from 1 and 15 October 2026, Acme's USD 0.04 override,
and the USD 20 monthly `concurrency_pack`. The grant, purchase, limit, measurements,
and invoices are actions performed during testing, rather than seeded actions.

Use the [API contract](../api/openapi.yaml),
[financial rules](../architecture/accounting-rules.md), and
[named public workflows](../simulator/billing-scenarios.md) for implementation
details. The original assignment is supplied separately; this guide reproduces its
required inputs and results without depending on private documentation files.

| Binding requirement | Verification and pass condition |
| --- | --- |
| Load customers, addresses, metric, historical prices, and add-on | Section 3 checks the literal seed values and empty accounts. |
| Transfer sandbox measurements with a platform substitute | Sections 4–5 send the six assignment increments; transport tests also verify splitting and retained buffers. |
| Price at consumption time; customer price overrides the default | October invoices use both Cyberdyne prices and Acme's unchanged override; the price-version workflow checks new versions. |
| E2B grants credit; expose remaining credit | Acme's balance moves through USD 25, 21, 13, and 7. The grant retry applies once. |
| Credit pays usage only | Acme pays the complete USD 20 add-on despite available credit; the exhaustion workflow checks partially covered usage. |
| Charge the full add-on price from its purchase month and every active month | Acme has a 2_000-cent add-on on both invoices; a separate month-end purchase costs the full price. |
| Customer can set a monthly spend limit; platform can read reached status | Cyberdyne is below/reached/below in October's first hour, second hour, and November respectively. |
| Limit uses gross usage, excludes add-ons, and never stops accounting | Section 8 combines credit, an add-on, and a finite limit. The limit workflow checks raised/zero/unlimited states; Cyberdyne is billed all 1_817 cents after exceeding its limit. |
| Monthly invoices in cents, with separate customer numbering | Acme receives `ACME-0001` and `ACME-0002`; Cyberdyne receives `CYBERDYNE-0001`. Lines sum to each total. |
| Late usage preserves historical pricing and an issued invoice | Late October usage appears on Acme's November invoice at 4 cents per million; the October snapshot stays identical. |
| Correctness through retries, delay, and unavailability | Sections 4 and 7 verify failed delivery, lost committed replies, durable receipt, immutable replay, and retained data after restart. Regression tests cover concurrent writers and transaction rollback. |
| Explain architecture, responsibilities, tools, unfinished work, and improvements | Section 9 reviews the delivered documentation and records remaining limitations. |

The implementation additionally chooses exact credit before rounding, cumulative
half-up rounding per price/usage/billing-month group, and credit allocation in
serialized processing order. Closing includes only groups already processed before it acquires the account lock; pending usage is billed later. Treat these as implementation decisions to review.
Do not infer a payment flow from the credit-grant endpoint: E2B supplies grants;
payment processing, taxes, and authentication are outside the assignment.

## 2. Automated regression gate in an isolated project

Use a clean checkout of the revision under review: Go's `./...` discovery can also
include unrelated local Go packages in ignored directories. Run commands from the
repository root in Bash, with Docker Compose available.
The walkthrough also uses `curl` and `jq`. Local Go is optional; Docker supplies
the pinned toolchain. Choose unused project names for a new acceptance run and
free ports. Retain the same project and state file when recovering a failed run.

The project names below deliberately allocate volumes separate from `e2b-local`.
They must be unused at the beginning of a new run. Changing only the simulator's
`SOURCE` or state filename does not reset customer balances or invoices.

```sh
set -euo pipefail
export COMPOSE_PROJECT_NAME=e2b-acceptance-tests-01
export E2B_POSTGRES_PORT=25432 E2B_API_PORT=28081 E2B_DOCS_PORT=28082
acceptance_evidence=$(mktemp -d "${TMPDIR:-/tmp}/e2b-acceptance.XXXXXX")
git rev-parse HEAD | tee "$acceptance_evidence/revision.txt"
git status --short | tee "$acceptance_evidence/working-tree.txt"
docker compose config --services
make up SERVICE=postgres
make migrate | tee "$acceptance_evidence/migrate-first.log"
make migration-status > "$acceptance_evidence/migrations-first.txt"
make migrate | tee "$acceptance_evidence/migrate-repeat.log"
make migration-status | tee "$acceptance_evidence/migrations.txt"
cmp "$acceptance_evidence/migrations-first.txt" "$acceptance_evidence/migrations.txt"
E2B_GO_CHECKS_DOCKER=1 make check 2>&1 | tee "$acceptance_evidence/check.log"
make test 2>&1 | tee "$acceptance_evidence/test.log"
```

Require exit status zero from both migration runs, `make check`, and `make test`.
The migration table has exactly versions 1 through 9, each once, with unchanged
`applied_at` on repeat. The fresh catalog is checked by the SQL seed suite.

`make test` runs SQL integrity in **both UTC and Asia/Shanghai**, Go package tests,
private-schema persistence tests, HTTP tests against the running API, and worker
process lifecycle tests. Require the named billing scenarios in section 6 to
actually execute and pass; a filtered or skipped suite is not the full gate.
SQL fixtures roll back, private schemas are dropped, and HTTP tests clean their
owned receipts. Keep the manual assignment in a separate fresh project anyway.

Check migration-runner serialization against the same database:

```sh
make migrate > "$acceptance_evidence/migrate-concurrent-1.log" 2>&1 &
acceptance_migrate_one=$!
make migrate > "$acceptance_evidence/migrate-concurrent-2.log" 2>&1 &
acceptance_migrate_two=$!
wait "$acceptance_migrate_one"
wait "$acceptance_migrate_two"
make migration-status > "$acceptance_evidence/migrations-concurrent.txt"
cmp "$acceptance_evidence/migrations.txt" "$acceptance_evidence/migrations-concurrent.txt"
```

Both runners must exit zero, with the same nine version rows and unchanged seed
values. Retain both logs. This exercises concurrent no-op runners after a fresh
migration; it does not demonstrate a failing future migration or simultaneous
first-time application of future versions.

The targeted commands in section 6 are diagnostic reruns in this test project;
the full gate already includes them. Finish those before starting the walkthrough.
Then free its ports while retaining test evidence and volumes:

```sh
make down
```

## 3. Fresh assignment environment and seed inspection

Select a second unused project and initialize it explicitly. `make up` starts the
API, worker, documentation portal, Scalar API reference, and idle simulator after the database migration.
Starting the idle simulator must create no usage.

```sh
export COMPOSE_PROJECT_NAME=e2b-acceptance-assignment-01
make up SERVICE=postgres
make migrate
make up
make ps
acceptance_api=http://127.0.0.1:28081
curl --fail-with-body "$acceptance_api/healthz"
curl --fail-with-body http://127.0.0.1:28082/reference/openapi.yaml \
  > "$acceptance_evidence/openapi-served.yaml"
cmp docs/api/openapi.yaml "$acceptance_evidence/openapi-served.yaml"
```

Expect healthy PostgreSQL, API, worker, docs, and api-docs; the simulator runs idle. Health
HTTP `200` verifies the API process. The seed query and financial reads verify
database access. Open Scalar at `http://127.0.0.1:28082/reference/`, check its reference, and
use its embedded request client for `GET /customers/acme/credit`; expect HTTP `200`
and an empty account. Browser requests go through the same docs service's proxy.

Inspect seed data through a separate database connection:

```sh
docker compose exec -T postgres psql -X -U e2b -d e2b_billing \
  -v ON_ERROR_STOP=1 <<'SQL' | tee "$acceptance_evidence/seed.txt"
SHOW timezone;
SELECT version, name FROM schema_migrations ORDER BY version;
SELECT customer_id, name, country, billing_address FROM customers ORDER BY customer_id;
SELECT metric FROM metrics;
SELECT price_version_id, customer_id, metric, price_per_million_cents, effective_from
FROM price_versions ORDER BY price_version_id;
SELECT addon_name, monthly_price_cents FROM addons;
SELECT customer_id, credit_balance_ticks, spend_limit_cents, state_version,
       next_invoice_number FROM customer_billing_state ORDER BY customer_id;
SELECT (SELECT count(*) FROM usage_inbox) AS receipts,
       (SELECT count(*) FROM credit_entries) AS credit_entries,
       (SELECT count(*) FROM addon_subscriptions) AS subscriptions,
       (SELECT count(*) FROM invoices) AS invoices;
SQL
```

Expect server time zone `UTC` and the following literal catalog:

| Record | Expected fields |
| --- | --- |
| `acme` | `Acme Inc.`, `US`, `1 Market St, San Francisco, CA 94105` |
| `cyberdyne` | `Cyberdyne Systems Corporation`, `US`, `18144 El Camino Real, Sunnyvale, CA 94087` |
| Metric | Exactly `cpu_seconds` |
| `cpu-default-2026-10-01` | Default (`customer_id` NULL), 5 cents per million, `2026-10-01T00:00:00Z` |
| `cpu-default-2026-10-15` | Default, 6 cents per million, `2026-10-15T00:00:00Z` |
| `cpu-acme-2026-10-01` | Acme override, 4 cents per million, `2026-10-01T00:00:00Z` |
| `concurrency_pack` | 2_000 cents per month |
| Each account | Zero ticks, NULL limit, `state_version=0`, `next_invoice_number=1` |
| Activity counts | Zero receipts, credit entries, subscriptions, and invoices |

Any different starting balance, version, activity, or catalog is a failed
precondition. Use another fresh isolated project rather than adapting expectations.

## 4. Walk through the complete assignment and transport failures

Use [billing-assignment.json](../simulator/billing-assignment.json) as the exact
request/response script. It has 24 named steps with literal financial expectations.
Each command below creates a new producer process while reusing its durable state.

```sh
export SOURCE=acceptance-assignment STATE=/state/acceptance-assignment.json
acceptance_next() {
  make simulate SCENARIO=billing-assignment MODE=step MAX_ATTEMPTS=10
}
```

`acceptance_next` executes exactly one observed step. Require its exit status zero
and `completed=N steps=24`, with `N` matching the row below. GET steps marked
`await` poll until their expected state is observed. A nonzero `processing_errors`
is a failure for this valid scenario. Do not replace polling with a fixed sleep.
An ERROR worker log with `priority=P0` and `error_code=missing_valid_price` identifies
a missing eligible catalog version. Use the [controlled SQL repair](../guides/accounting-recovery.md#controlled-historical-price-repair),
then explicitly release the receipt using the [recovery procedure](../guides/accounting-recovery.md#investigate-and-release-a-receipt).
Ordinary `POST /prices` rejects new backdated versions; invoice issuance never
repairs or processes the receipt.
The assignment seed's effective starts remain unchanged; additional metrics must
be priced before their first measurement, and `effective_from` cannot be null.
Use explicit month paths for October/November; `/limit-status` uses the real
current UTC month. Future fixture timestamps do not advance the server clock.

### Unavailable API before step 1

```sh
make stop SERVICE=api
if make simulate SCENARIO=billing-assignment MODE=step MAX_ATTEMPTS=1; then
  printf '%s\n' 'FAIL: unavailable API unexpectedly completed a step' >&2
  exit 1
fi
make simulate SCENARIO=billing-assignment ACTION=status
make up SERVICE=api
acceptance_next
acceptance_next
```

The failed process must report a transport error and retain `completed=0 steps=24`.
No financial action occurred. The last two commands pass steps 1 and 2 against the
restarted API, with both accounts still zero. A failed assertion, invalid config,
or failed container build is not evidence of the expected network failure.

### Lost committed grant reply at step 3

```sh
if make simulate SCENARIO=billing-assignment MODE=step MAX_ATTEMPTS=1; then
  printf '%s\n' 'FAIL: intentionally lost grant reply unexpectedly completed' >&2
  exit 1
fi
make simulate SCENARIO=billing-assignment ACTION=status
curl --fail-with-body "$acceptance_api/customers/acme/credit" | jq .
acceptance_next
acceptance_next
acceptance_next
```

Require `injected lost committed response`, with `completed=2 steps=24` despite the
successful grant commit. The credit read shows 2_500_000_000 ticks, version 1, and
zero pending/errors. The next process confirms the same `welcome` operation and
completes step 3 without another grant. The following commands purchase the pack
and set the limit, ending at `completed=5`. The workflow later loses an invoice
reply and a late-usage reply too; ordinary retries must preserve their results.

### Durable receipt while the worker is stopped at step 6

```sh
make stop SERVICE=worker
acceptance_next
curl --fail-with-body "$acceptance_api/customers/acme/credit" | jq .
curl --fail-with-body \
  "$acceptance_api/customers/cyberdyne/months/2026-10/limit-status" | jq .
make up SERVICE=worker
```

Step 6 returns durable HTTP `202`, with `completed=6`. Acme still has
2_500_000_000 ticks, version 2, one pending event, and zero errors. Cyberdyne has
zero gross ticks, version 1, one pending event, and zero errors. Both inbox rows
have `processed_at` and `processing_error` NULL, and no rating link yet. Read them
with the receipt query in section 5 if inspecting this checkpoint in SQL.
Restarting the worker must account for both already accepted increments.

### Receipt identity and metadata at steps 7–9

Complete the two accounting reads, snapshot the first receipts, and repeat them:

```sh
acceptance_next
acceptance_next
acceptance_first_receipts() {
  docker compose exec -T postgres psql -X -U e2b -d e2b_billing \
    -v ON_ERROR_STOP=1 -At --command="SELECT to_jsonb(i) FROM usage_inbox i
      WHERE source = 'acceptance-assignment'
        AND event_id IN ('acme-first', 'cyberdyne-first') ORDER BY event_id"
}
acceptance_first_receipts > "$acceptance_evidence/receipts-before-retry.jsonl"
acceptance_next
acceptance_first_receipts > "$acceptance_evidence/receipts-after-retry.jsonl"
cmp "$acceptance_evidence/receipts-before-retry.jsonl" \
    "$acceptance_evidence/receipts-after-retry.jsonl"
```

Require two processed rows before replay and an identical comparison afterward,
including their `received_at` and `processed_at`. The checkpoint is now 9.
The independent accounting tests also check unchanged financial effects.

### Remaining steps and expected checkpoints

Run `acceptance_next` once for each remaining step 10 through 24, reviewing the
result against this table. Rows 1 through 9 describe the commands already run.
Financial commands return HTTP `200`; usage sends and retries return `202`.

| Completed step | Action/input | Required observation |
| --- | --- | --- |
| 1–2 | Read both fresh accounts | Balance 0; pending/errors 0; version 0. |
| 3 | Grant Acme 2_500 cents, operation `welcome`, audit time `2026-10-01T00:00:00Z` | One grant despite the lost reply; balance 2_500_000_000 ticks. |
| 4 | Buy `concurrency_pack`, subscription `acme-pack`, at `2026-10-05T00:00:00Z` | Price snapshot 2_000 cents, start month `2026-10-01`; credit unchanged. |
| 5 | Set Cyberdyne's limit to 1_500 cents, operation `october-limit` | Current configuration is 1_500 cents. |
| 6 | Send first hour `[2026-10-10T12:00:00Z, 13:00:00Z)` | Acme 100_000_000 units; Cyberdyne 123_456_789 units. Two durable receipts. |
| 7 | Await Acme accounting | Credit 2_100_000_000 ticks (USD 21); version 3; pending/errors 0. |
| 8 | Read Cyberdyne October status | Gross 617_283_945 ticks, limit 1_500 cents, reached `false`; pending/errors 0. |
| 9 | Repeat both first-hour event identities and content | HTTP `202`; receipt timestamps, balances, gross totals, and rating counts remain unchanged. |
| 10 | Send second hour `[2026-10-20T12:00:00Z, 13:00:00Z)` | Each customer 200_000_000 units; two additional receipts. |
| 11 | Await Acme accounting | Credit 1_300_000_000 ticks (USD 13); version 4; pending/errors 0. |
| 12 | Read Cyberdyne October status | Gross 1_817_283_945 ticks, reached `true`; pending/errors 0. |
| 13 | Issue Acme October (`month: 2026-10`), losing its committed reply | `ACME-0001`, total 2_000 cents; capture complete snapshot. |
| 14 | Issue Cyberdyne October | `CYBERDYNE-0001`, total 1_817 cents, including all measured usage above the limit. |
| 15 | Deliver late Acme hour `[2026-10-30T12:00:00Z, 13:00:00Z)`, 50_000_000 units, with a lost reply | One additional receipt; October price retained, invoice month November. |
| 16 | Send Acme hour `[2026-11-03T12:00:00Z, 13:00:00Z)`, 100_000_000 units | One additional receipt in November. |
| 17 | Await Acme accounting | Credit 700_000_000 ticks (USD 7); version 7; pending/errors 0. |
| 18 | Read Cyberdyne November status | Gross 0, same 1_500-cent limit, reached `false`; pending/errors 0. |
| 19 | Issue Acme November | `ACME-0002`, total 2_000 cents; separate late October and November usage lines. |
| 20 | Read Acme credit after issuance | Still 700_000_000 ticks; version 8; pending/errors 0. Issuance debits no credit. |
| 21–22 | Read and reissue Acme October | Complete response equals its captured October snapshot, including `issued_at` and number. |
| 23 | Read Acme November | Complete response equals its captured November snapshot. |
| 24 | Read Cyberdyne October | Complete response equals its captured October snapshot. |

Record the completed checkpoint and retain the producer state:

```sh
make simulate SCENARIO=billing-assignment ACTION=status \
  | tee "$acceptance_evidence/assignment-status.txt"
docker compose exec -T simulator cat /state/acceptance-assignment.json \
  > "$acceptance_evidence/assignment-state.json"
jq -e '.next_step == 24 and .step_count == 24 and (.last_error // "") == ""' \
  "$acceptance_evidence/assignment-state.json"
```

For an unattended replay on another fresh assignment project, one
`make simulate SCENARIO=billing-assignment MAX_ATTEMPTS=10` completes all 24 steps.
It does not reproduce the explicit stopped-service checkpoints above. Running it
on this completed state sends no further actions; a new state on used accounts
fails the fresh-account precondition.

## 5. Final invoices and independently persisted state

Compare all invoice fields with the seed buyer identity, `currency: USD`, explicit
month, customer-specific number, exact audit ticks, and these literal lines:

| Invoice | Ordered line amounts, cents | Gross usage ticks | Credit used ticks | Total cents |
| --- | --- | --- | --- | --- |
| Acme October, `ACME-0001` | Usage 1_200; pack 2_000; credit −1_200 | 1_200_000_000 | 1_200_000_000 | 2_000 |
| Cyberdyne October, `CYBERDYNE-0001` | Usage at 5 cents: 617; at 6 cents: 1_200; credit 0 | 1_817_283_945 | 0 | 1_817 |
| Acme November, `ACME-0002` | Late October usage 200; November usage 400; pack 2_000; credit −600 | 600_000_000 | 600_000_000 | 2_000 |

Cyberdyne's first charge is exactly USD 6.172_839_45 before presentation rounding.
Acme's November usage lines retain `usage_month: 2026-10` and `2026-11` respectively;
the late line is labeled `Late usage from 2026-10`. Every invoice's displayed line
amounts sum exactly to its total. Zero credit remains an explicit zero-cent line
for Cyberdyne.

Use a separate PostgreSQL session to inspect the records, rather than deriving
expected values using production accounting functions:

```sh
docker compose exec -T postgres psql -X -U e2b -d e2b_billing \
  -v ON_ERROR_STOP=1 <<'SQL' | tee "$acceptance_evidence/final-state.txt"
SELECT event_id, customer_id, units, received_at, processed_at, processing_error
FROM usage_inbox WHERE source = 'acceptance-assignment' ORDER BY event_id;
SELECT i.event_id, g.customer_id, g.price_version_id, g.usage_month, g.billing_month
FROM usage_ratings r JOIN usage_inbox i USING (source, event_id)
JOIN rated_usage_groups g USING (group_id)
WHERE r.source = 'acceptance-assignment' ORDER BY i.event_id;
SELECT customer_id, price_version_id, usage_month, billing_month, total_units,
       exact_charge_ticks, allocated_credit_ticks, booked_charge_cents
FROM rated_usage_groups ORDER BY customer_id, billing_month, usage_month, price_version_id;
SELECT customer_id, usage_month, gross_charge_ticks
FROM monthly_usage ORDER BY customer_id, usage_month;
SELECT s.customer_id, s.credit_balance_ticks, s.spend_limit_cents, s.state_version,
       s.next_invoice_number, COALESCE(sum(e.amount_ticks), 0) AS ledger_balance
FROM customer_billing_state s LEFT JOIN credit_entries e USING (customer_id)
GROUP BY s.customer_id ORDER BY s.customer_id;
SELECT customer_id, subscription_id, addon_name, monthly_price_cents, start_month
FROM addon_subscriptions;
SELECT customer_id, billing_month, invoice_number, total_cents,
       snapshot->>'customer_name' AS buyer, snapshot->>'country' AS country,
       snapshot->>'billing_address' AS address
FROM invoices ORDER BY customer_id, billing_month;
SELECT (SELECT count(*) FROM invoices) AS invoices,
       (SELECT count(*) FROM closed_billing_months) AS closed_months,
       (SELECT count(*) FROM invoiced_usage_groups) AS frozen_groups,
       (SELECT count(*) FROM credit_entries) AS credit_entries;
SQL
```

Require six receipts and six rating links, including exactly one each for
`acme-first`, `acme-second`, `acme-late`, `acme-november`, `cyberdyne-first`, and
`cyberdyne-second`. All six receipts are processed with no error. First receipt
timestamps must survive identical retries. The five groups are:

| Customer / price | Usage month → invoice month | Units | Gross ticks | Credit ticks | Booked gross cents |
| --- | --- | --- | --- | --- | --- |
| Acme / `cpu-acme-2026-10-01` | October → October | 300_000_000 | 1_200_000_000 | 1_200_000_000 | 1_200 |
| Acme / `cpu-acme-2026-10-01` | October → November | 50_000_000 | 200_000_000 | 200_000_000 | 200 |
| Acme / `cpu-acme-2026-10-01` | November → November | 100_000_000 | 400_000_000 | 400_000_000 | 400 |
| Cyberdyne / `cpu-default-2026-10-01` | October → October | 123_456_789 | 617_283_945 | 0 | 617 |
| Cyberdyne / `cpu-default-2026-10-15` | October → October | 200_000_000 | 1_200_000_000 | 0 | 1_200 |

Acme's stored October monthly gross is 1_400_000_000 ticks, including late usage;
its November monthly gross is 400_000_000 ticks. Cyberdyne's October gross is
1_817_283_945 ticks. Its November row is absent and the API projects zero. An
invoice's gross can therefore differ from its original month's gross.

Require Acme balance/ledger 700_000_000 ticks, NULL limit, version 8, and next number
3. Cyberdyne has balance/ledger 0, limit 1_500 cents, version 4, and next number 2.
There is one Acme subscription, three invoices, three closed months, five frozen
groups, and five credit entries: one 2_500_000_000-tick grant and four usage debits
of 400_000_000, 800_000_000, 200_000_000, and 400_000_000 ticks. No credit entry pays
an add-on or is created by invoice issuance. Buyer details match the seed table.

## 6. Remaining business, integrity, and concurrency scenarios

The full regression gate must also pass each case below. Financial workflow
fixtures run in independently seeded private schemas with a real router and
worker. When manually replaying any additional JSON workflow, use a new seeded
project for **each** file. Reusing the assignment accounts or changing only source
would invalidate its literal expected results.

The fixed `billing-price-versions` workflow must use the private-schema automated
runner. It declares `2026-10-01T00:00:00Z` for its first two price commands and
`2026-12-01T00:00:00Z` for its two rejected backdated commands and invoice. A live
December server cannot create its October versions through ordinary `POST /prices`;
changing only measurement dates does not advance or override the server clock.

Section 8 adds a required manual combination of credit, an add-on, and a finite
limit. The existing public limit workflow uses a customer without credit; the
combined check must be recorded separately from the automated suite.

| Scenario / regression entry point | Inputs and required outcome |
| --- | --- |
| [Command retries](../simulator/billing-command-retries.json), 16 steps | Grant, purchase, limit, and price retries preserve original results. Changed grant/price content and a second pack subscription return `409`. Replaying the old 1_500-cent limit after raising it to 2_000 preserves the newer setting. Final Acme balance is 2_500_000_000 ticks, version 4. |
| [Price versions](../simulator/billing-price-versions.json), 8 steps | Append an 8-cent default effective 12 October and a 3-cent Cyberdyne override effective 19 October. Two 100_000_000-unit increments on 13/20 October produce 800 + 300 cents and `CYBERDYNE-0001` total 1_100. Both new backdated versions return `422` / `invalid_command` with field `effective_from`; no price or financial history changes. |
| [Exact credit](../simulator/billing-exact-credit.json), 6 steps | One cent of credit and two 1_250-unit Acme increments consume 10_000 ticks, leaving 990_000. A zero-cent invoice retains both exact audit amounts; issuance consumes no extra ticks. |
| [Credit exhaustion](../simulator/billing-credit-exhaustion.json), 9 steps | One cent covers part of 500_000 Acme units (2 cents gross). A pack bought `2026-10-31T23:59:59Z` still costs 2_000 cents. Invoice total is 2_001; credit becomes zero. A later 5-cent grant leaves 5_000_000 ticks and cannot change the captured invoice. |
| [Limit status](../simulator/billing-limit-status.json), 15 steps | Gross crosses 1_500 cents, a raise to 2_000 clears it, a new UTC month resets gross, zero is reached even without usage, and NULL means unlimited. Every measured unit is still invoiced: 1_817 cents. |
| [Price boundary](../simulator/billing-price-boundary.json), 5 steps | Cyberdyne interval `2026-10-14T23:30Z`–`2026-10-15T00:30Z`, 100 units: receipt `202`, then one processing error, no charge, invoice POST `200` with zero usage total, invoice GET `200`. |
| [Month boundary](../simulator/billing-month-boundary.json), 5 steps | Acme interval `2026-10-31T23:30Z`–`2026-11-01T00:30Z`, 100 units: the same visible error/no-charge outcome and successful empty invoice. |
| [Usage HTTP tests](../../tests/api/usage_batches_test.go) and [repository tests](../../tests/inbox/usage_inbox_test.go) | Identical concurrent batches insert once; changed content conflicts and rolls back the entire batch, including a fresh preceding event. Preserve original receipt and processing metadata. Invalid later input causes no partial acceptance. Test transaction cancellation, lock waits, opposite ordering, and commit/rollback races. |
| [Accounting processing](../../tests/inbox/billing_test.go), [missing-price recovery](../../tests/inbox/missing_price_test.go) | Two 60_000-unit default-price increments form 600_000 ticks and one rounded cent, rather than rounding each receipt. Concurrent workers/replay charge once. A late database failure rolls back all financial effects. Missing valid prices commit a P0 error without charges, log once, and recover only after a dated price and explicit release; a failed quarantine commit retains pending input without a committed-incident report. Unsupported schema 2 produces no rating or debit and remains a visible error. |
| [Money and calendar primitives](../../internal/accounting/money_test.go), [UTC calendar tests](../../internal/accounting/calendar_test.go) | A limit is reached at equality using exact gross ticks, including credit-covered usage. Credit stays nonnegative; half-cent ties use half-up presentation. Month calculations normalize offsets to UTC, and add-ons cost zero before their purchase month. |
| [Invoice closing](../../tests/inbox/invoices_test.go), [processed cutoff](../../tests/inbox/processed_closing_test.go), and [first-month UTC boundary](../../tests/inbox/first_closing_test.go) | Eight concurrent close requests produce one `ACME-0001`. Only already processed groups enter a one-transaction invoice. Pending and quarantined usage in the target month stay untouched and do not block issuance. The first close cannot skip earlier existing usage; later new invoices require the preceding month closed (`409` on conflict). Both worker/closing lock orders preserve exactly-once charges. Later worker processing and recovery route forward without changing the original invoice; publication failure rolls back closure, freezing and numbering. Empty months are valid; invalid months return `422`, missing invoices/customers `404`. |
| [Grant](../../tests/inbox/credit_test.go), [purchase](../../tests/inbox/addons_test.go), and [price](../../tests/inbox/prices_test.go) | Changed operation content, missing catalog/customers, and retroactive changes produce their declared errors with no extra grant, subscription, or price. A new purchase cannot alter a closed month. |
| [Simulator generation](../../internal/simulator/scenario_test.go) and [transport executable](../../tests/api/simulator_test.go) | Splitting across sandboxes/minutes preserves the assignment totals; offline generation, failed send, restart, delivery barriers, reversed retries, and replay retain the intended events. Replayed delivery preserves the inbox. |
| [Worker lifecycle](../../tests/worker/lifecycle_test.go) and [SQL integrity](../../tests/sql/) | SIGTERM drains/stops within the configured budget and cleans its heartbeat; invalid config fails startup. SQL rejects invalid required fields, money, group ownership, rating links, and attempts to mutate append-only history in both time zones. |

Use these existing filters to investigate a failed requirement in the automated
project; do not substitute them for the full gate:

```sh
make test RUN='^TestBillingSimulatorExecutable$'
make test RUN='^TestCloseMonth$/^concurrent_closing_allocates_one_number$'
make test RUN='^TestProcessBatch$'
make test RUN='^TestPostUsageBatches$|^TestPostgresInsertBatch$'
make test RUN='^TestWorkerExecutable$'
```

`RUN` selects Go tests; `SUITE=db` cannot be combined with it. Named-subtest spaces
become underscores in Go output. If an intended subtest never appears, the filter
has not validated it. The billing executable suite additionally requires recovery
after two `503` responses with `Retry-After`, restart against an unavailable API,
and recovery of a lost committed grant in a new process.

`TestSpendLimitAPI` checks that the current-month route responds successfully.
Independently check its returned month in the completed assignment project:

```sh
acceptance_current_month=$(date -u +%Y-%m)
curl --fail-with-body "$acceptance_api/customers/cyberdyne/limit-status" \
  | jq -e --arg month "$acceptance_current_month" \
    '.month == $month and .pending_events == 0 and .processing_errors == 0'
```

Require `true`, using a host clock synchronized with the server. If UTC rolls into
a new month during the request, sample and read again. The explicit November read
above verifies the new-month business calculation without changing clocks. Neither
case demonstrates an automatic invoice scheduler.

## 7. Persistence and immutable reads across restart

After the assignment finishes, capture all three full JSON invoices. Use canonical
JSON for equality so formatting/key order does not affect the comparison:

```sh
for acceptance_invoice in acme/2026-10 acme/2026-11 cyberdyne/2026-10; do
  acceptance_customer=${acceptance_invoice%/*}
  acceptance_month=${acceptance_invoice#*/}
  curl --fail-with-body \
    "$acceptance_api/customers/$acceptance_customer/invoices/$acceptance_month" \
    | jq -S . > "$acceptance_evidence/$acceptance_customer-$acceptance_month-before.json"
done
make stop
make up
make migrate
make simulate SCENARIO=billing-assignment ACTION=status
for acceptance_invoice in acme/2026-10 acme/2026-11 cyberdyne/2026-10; do
  acceptance_customer=${acceptance_invoice%/*}
  acceptance_month=${acceptance_invoice#*/}
  curl --fail-with-body \
    "$acceptance_api/customers/$acceptance_customer/invoices/$acceptance_month" \
    | jq -S . > "$acceptance_evidence/$acceptance_customer-$acceptance_month-after.json"
  cmp "$acceptance_evidence/$acceptance_customer-$acceptance_month-before.json" \
      "$acceptance_evidence/$acceptance_customer-$acceptance_month-after.json"
done
curl --fail-with-body "$acceptance_api/customers/acme/credit" | jq .
curl --fail-with-body \
  "$acceptance_api/customers/cyberdyne/months/2026-10/limit-status" | jq .
```

Require three successful comparisons, `completed=24 steps=24`, Acme's unchanged
700_000_000-tick balance/version 8, and Cyberdyne's unchanged reached October
status/version 4. Rerun the section 5 SQL queries: counts, totals, ledger, frozen
groups, and invoice numbers must remain identical. Migrations must add no new
version or activity. This checks retained volumes and graceful process restart;
it does not simulate a power failure or lost/corrupted disk.

## 8. Credit-covered gross spend and add-on exclusion

After finishing the restart check, keep that assignment's volumes and select a
third unused project. This case combines two binding rules in one public-API
workflow: an add-on alone cannot reach a usage limit, and credit cannot reduce the
gross usage compared with that limit.

```sh
acceptance_assignment_project=$COMPOSE_PROJECT_NAME
make down
export COMPOSE_PROJECT_NAME=e2b-acceptance-credit-limit-01
make up SERVICE=postgres
make migrate
make up
```

Verify the empty Acme account as in section 3. Submit these requests through
Scalar's embedded client at `http://127.0.0.1:28082/reference/`, or send the identical JSON
with `curl` to `$acceptance_api`. Save responses and status codes in the evidence
directory. The customer path in every financial request is `/customers/acme`.

| Step | Method/path and explicit input | Required result |
| --- | --- | --- |
| Grant | POST `/customers/acme/credits`, `{"operation_id":"covered-grant","amount_cents":2500,"recorded_at":"2026-10-01T00:00:00Z"}` | HTTP `200`, balance 2_500_000_000 ticks. |
| Purchase | POST `/customers/acme/addons`, `{"subscription_id":"covered-pack","addon_name":"concurrency_pack","purchased_at":"2026-10-05T00:00:00Z"}` | HTTP `200`, monthly price 2_000 cents. |
| Limit | POST `/customers/acme/spend-limit`, `{"operation_id":"covered-limit","limit_cents":1500}` | HTTP `200`, limit 1_500 cents. |
| Before usage | GET `/customers/acme/months/2026-10/limit-status` | HTTP `200`, gross 0, reached `false`, pending/errors 0. The 2_000-cent pack exceeds the limit but is excluded. |
| Usage | POST `/usage/batches` with the JSON below | HTTP `202`; all 400_000_000 units are retained even though their gross charge exceeds the limit. |
| Await accounting | Poll GET `/customers/acme/months/2026-10/limit-status` | Gross 1_600_000_000 ticks, version 4, pending/errors 0. The worker must finish before the next step to assert this exact invoice. |
| Close | POST `/customers/acme/invoices`, `{"month":"2026-10"}` | HTTP `200`, `ACME-0001`, usage 1_600 cents + pack 2_000 − credit 1_600 = total 2_000. Exact gross and used credit are both 1_600_000_000 ticks. Closing reads the already processed group. |
| After closing | GET credit and October limit status | Balance 900_000_000 ticks, version 5; gross 1_600_000_000 ticks, limit 1_500 cents, reached `true`, pending/errors 0. Usage is fully credit-covered yet reaches the limit. |
| Raise | POST `/customers/acme/spend-limit`, `{"operation_id":"covered-raised","limit_cents":2000}`, then GET October status | HTTP `200`, unchanged gross 1_600_000_000 ticks, limit 2_000 cents, reached `false`, version 6. |

```json
{
  "events": [
    {
      "source": "acceptance-credit-limit",
      "event_id": "covered-usage",
      "schema_version": 1,
      "customer_id": "acme",
      "sandbox_id": "covered-sandbox",
      "metric": "cpu_seconds",
      "period_start": "2026-10-10T12:00:00Z",
      "period_end": "2026-10-10T13:00:00Z",
      "units": 400000000
    }
  ]
}
```

Inspect this project's `monthly_usage`, `credit_entries`, and invoice snapshot
through PostgreSQL as in section 5: one 1_600_000_000-tick gross usage entry, a
2_500_000_000-tick grant, one −1_600_000_000-tick debit, and the exact invoice above.
Record this case's PASS/FAIL separately. Return to the saved assignment environment
for review without mixing their accounts:

```sh
make down
export COMPOSE_PROJECT_NAME=$acceptance_assignment_project
make up
```

## 9. Architecture review, limitations, and completion record

Review the [submission overview](../architecture/submission-overview.md),
[selected architecture and workload assumptions](../brainstorming/architecture-options.md),
[usage-to-invoice flow](../architecture/usage-to-invoice.md),
[financial rules](../architecture/accounting-rules.md), and
[interface ownership](../architecture/submission-overview.md#interfaces-and-ownership). Record:

- Why the separate platform sends durable, idempotent increments into PostgreSQL
  and why asynchronous accounting uses customer locks and explicit commit boundaries.
- Why the platform polls limit status, how pending/error counts affect freshness,
  and why platform enforcement after observing the signal belongs to its own team.
- Which interfaces belong to platform usage/limit reads, E2B price/grant/closing
  operations, and customer add-on/limit/balance/invoice actions. Roles are conceptual;
  no authorization enforcement is implemented in this assignment.
- Why Go, PostgreSQL, Compose, and the durable simulator were selected, and whether
  the documented design addresses about 50_000 customers with several through
  thousands of concurrent sandboxes and minute records. Review the scaling proposal
  and remaining operational decisions; this procedure does not establish
  billion-record capacity or a
  measured production throughput/freshness guarantee.

Keep these implementation limits visible in the acceptance result: a receipt must
fit one UTC month and one applicable price version; unsupported crossings remain
errors rather than invented allocations. Invoice issuance is explicitly invoked
through the API. Month ordering, half-up grouping, and credit allocation order
are implementation decisions. Automatic calendar scheduling, broader interval
segmentation, additional correction policies, and production scaling remain
follow-ups. Closing includes only accounting committed before it acquired the
customer account lock; pending and quarantined target-month input never waits
inside publication. The first invoice cannot skip earlier known usage, and later
new invoices require a closed predecessor. Processing after closure routes forward
without changing issued invoices and can introduce separate rounding groups.

Record the commit and any working-tree changes, date, Compose project names,
commands and exit results, full-suite logs, completed scenario counts, seed and
final SQL output, captured invoices, and restart comparisons. For each requirement
in section 1 mark **PASS**, **FAIL**, or **NOT RUN**, with an evidence location and
any limitation needing review. Expected failure injection is a pass only when its
specific saved state, unchanged financial effect, and successful recovery match.
The run is complete when every required scenario has evidence and no unexplained
financial mismatch, pending valid event, or processing error remains. An unresolved
failure or an unexecuted requirement prevents a clean acceptance result.

Keep the assignment environment available for review, or stop it with `make down`
while retaining its volumes. Evidence is outside the tracked repository. If later
removing test volumes, select only the recorded acceptance project; do not reuse
cleanup commands against a project containing data you need.
