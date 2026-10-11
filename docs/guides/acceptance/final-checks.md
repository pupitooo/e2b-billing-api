# Acceptance: final checks

Part 4 of 4 in the [acceptance procedure](../final-acceptance-testing.md).
Keep the same shell session, variables, helper functions, project selection,
and evidence directory throughout the procedure. Resets occur only at the
existing checkpoints 2.5, 7.4, and 8.4, after saving evidence.
Complete [Assignment walkthrough](assignment.md) before continuing here.

## 6. Remaining business, integrity, and concurrency scenarios

**Initial state**

- Automated diagnostic reruns select section 2's project with its migrated database
  and run before that project is reset for the manual walkthrough.
- Financial workflow fixtures start in independently seeded isolated schemas with
  a real router and worker.
- Each additional manual JSON workflow starts after the reset in step 1.5, with
  a freshly seeded database and no simulator progress. Reset again after it
  completes. Changing only source does not reset the accounts.
- A live workflow's invoice months must already be completed UTC months.
- The fixed `billing-price-versions` workflow uses the isolated-schema automated
  runner. Its first two price commands see `2026-10-01T00:00:00Z`; its two rejected
  backdated commands and invoice see `2026-12-01T00:00:00Z`. A live December server
  cannot create those October versions through ordinary `POST /prices`; measurement
  dates cannot advance or override the server clock.

| Step | Action and inputs | Expected result |
| --- | --- | --- |
| [6.1](#61-check-that-every-required-regression-scenario-actually-ran) | Inspect step 2.3's `test.log` for each regression entry below. | Every required scenario has a passing result; missing/skipped cases are **NOT RUN**, failures require investigation. |
| [6.2](#62-investigate-a-failing-scenario-with-its-existing-filter) | If needed, rerun the listed filters after step 2.3 and before reset 2.5. | The intended named subtest actually appears and its result is saved; diagnostic reruns do not replace the full gate. |
| [6.3](#63-verify-the-current-month-public-api-response) | After section 5, read Cyberdyne's `/limit-status` in the assignment project and compare with synchronized host UTC month. | HTTP `200`; `jq` prints `true`; returned month matches, with zero pending events and processing errors. |

### 6.1 Check that every required regression scenario actually ran

Open the `test.log` saved in step 2.3. Find each scenario below and its passing
result. Record a missing or skipped scenario as **NOT RUN**; investigate a failed
scenario using step 6.2 in the automated project before continuing.

The full regression gate must also pass each case below.

Record the [manual scenario in section 8](#82-execute-each-request-and-compare-its-response)
separately from the automated suite, using its numbered step table.

| Scenario / regression entry point | Inputs and required outcome |
| --- | --- |
| [Command retries](../../simulator/billing-scenarios.md#command-retries-and-conflicts), 16 steps | Compare every step with the canonical action and expected-result table. |
| [Price versions](../../simulator/billing-scenarios.md#price-versions), 8 steps | Compare every step with the canonical action and expected-result table, including its insertion-clock prerequisites. |
| [Exact credit](../../simulator/billing-scenarios.md#exact-sub-cent-credit), 6 steps | Compare every step with the canonical action and expected-result table. |
| [Credit exhaustion](../../simulator/billing-scenarios.md#credit-exhaustion), 9 steps | Compare every step with the canonical action and expected-result table. |
| [Limit status](../../simulator/billing-scenarios.md#monthly-limit-status), 15 steps | Compare every step with the canonical action and expected-result table. |
| [Price boundary](../../simulator/billing-scenarios.md#price-boundary), 5 steps | Compare every step with the canonical action and expected-result table. |
| [Month boundary](../../simulator/billing-scenarios.md#utc-month-boundary), 5 steps | Compare every step with the canonical action and expected-result table. |
| [Usage HTTP tests](../../../tests/api/usage_batches_test.go) and [repository tests](../../../tests/inbox/usage_inbox_test.go) | Identical concurrent batches insert once; changed content conflicts and rolls back the entire batch, including a fresh preceding event. Preserve original receipt and processing metadata. Invalid later input causes no partial acceptance. Test transaction cancellation, lock waits, opposite ordering, and commit/rollback races. |
| [Accounting processing](../../../tests/inbox/billing_test.go), [missing-price recovery](../../../tests/inbox/missing_price_test.go) | Two 60_000-unit default-price increments form 600_000 microcents and one rounded cent, rather than rounding each receipt. Concurrent workers/replay charge once. A late database failure rolls back all financial effects. Missing valid prices commit a P0 error without charges, log once, and recover only after a dated price and explicit release; a failed quarantine commit retains pending input without a committed-incident report. Unsupported schema 2 produces no rating or debit and remains a visible error. |
| [Money and calendar primitives](../../../internal/accounting/money_test.go), [UTC calendar tests](../../../internal/accounting/calendar_test.go) | A limit is reached at equality using exact gross microcents, including credit-covered usage. Credit stays nonnegative; half-cent ties use half-up presentation. Month calculations normalize offsets to UTC, and add-ons cost zero before their purchase month. |
| [Invoice closing](../../../tests/inbox/invoices_test.go), [processed cutoff](../../../tests/inbox/processed_closing_test.go), and [first-month UTC boundary](../../../tests/inbox/first_closing_test.go) | Eight concurrent close requests produce one `ACME-0001`. Only already processed groups enter a one-transaction invoice. Pending and quarantined usage in the target month stay untouched and do not block issuance. The first close cannot skip earlier existing usage; later new invoices require the preceding month closed (`409` on conflict). Both worker/closing lock orders preserve exactly-once charges. Later worker processing and recovery route forward without changing the original invoice; publication failure rolls back closure, freezing and numbering. Empty months are valid; invalid months return `422`, missing invoices/customers `404`. |
| [Grant](../../../tests/inbox/credit_test.go), [purchase](../../../tests/inbox/addons_test.go), and [price](../../../tests/inbox/prices_test.go) | Changed operation content, missing catalog/customers, and retroactive changes produce their declared errors with no extra grant, subscription, or price. A new purchase cannot alter a closed month. |
| [Simulator generation](../../../internal/simulator/scenario_test.go) and [transport executable](../../../tests/api/simulator_test.go) | Splitting across sandboxes/minutes preserves the assignment totals; offline generation, failed send, restart, delivery barriers, reversed retries, and replay retain the intended events. Replayed delivery preserves the inbox. |
| [Worker lifecycle](../../../tests/worker/lifecycle_test.go) and [SQL integrity](../../../tests/sql) | SIGTERM drains/stops within the configured budget and cleans its heartbeat; invalid config fails startup. SQL rejects invalid required fields, money, group ownership, rating links, and attempts to mutate append-only history in both time zones. |

### 6.2 Investigate a failing scenario with its existing filter

Use these existing filters to investigate a failed requirement in the automated
project; do not substitute them for the full gate:

```sh
(
  set -euo pipefail
  make test RUN='^TestBillingSimulatorExecutable$'
  make test RUN='^TestCloseMonth$/^concurrent_closing_allocates_one_number$'
  make test RUN='^TestProcessBatch$'
  make test RUN='^TestPostUsageBatches$|^TestPostgresInsertBatch$'
  make test RUN='^TestWorkerExecutable$'
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

`RUN` selects Go tests; `SUITE=db` cannot be combined with it. Named-subtest spaces
become underscores in Go output. If an intended subtest never appears, the filter
has not validated it. The billing executable suite additionally requires recovery
after two `503` responses with `Retry-After`, restart against an unavailable API,
and recovery of a lost committed grant in a new process.

### 6.3 Verify the current-month public API response

Run this step in the assignment project after section 5.

**Initial state**

- Section 4's assignment has completed in `e2b-acceptance-01`, with
  the API running and zero pending events or processing errors.
- The shell session selects that assignment project and retains `acceptance_api`.
- The host clock is synchronized with the server clock.

`TestSpendLimitAPI` checks that the current-month route responds successfully.
Independently check its returned month in this completed assignment project:

```sh
(
  set -euo pipefail
  acceptance_current_month=$(date -u +%Y-%m)
  curl --fail-with-body "$acceptance_api/customers/cyberdyne/limit-status" \
    | jq -e --arg month "$acceptance_current_month" \
      '.month == $month and .pending_events == 0 and .processing_errors == 0'
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

If UTC rolls into a new month during the request, sample and read again. The
explicit November assignment read checks the new-month business calculation.
Neither case demonstrates an automatic invoice scheduler.


The two blocks below have different starting states. Run diagnostic filters
after step 2.3 and before step 2.5; run the current-month read after section 5
in the assignment environment.

<details>
<summary>Complete commands: optional diagnostic reruns before the automated project reset</summary>

```sh
set +e
set +u
set -o pipefail

acceptance_run_diagnostic_filters() {
  (
    set -euo pipefail
    make test RUN='^TestBillingSimulatorExecutable$'
    make test RUN='^TestCloseMonth$/^concurrent_closing_allocates_one_number$'
    make test RUN='^TestProcessBatch$'
    make test RUN='^TestPostUsageBatches$|^TestPostgresInsertBatch$'
    make test RUN='^TestWorkerExecutable$'
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"
}

acceptance_run_diagnostic_filters
acceptance_scenario_status=$?
printf 'Scenario exit status: %s\n' "$acceptance_scenario_status"
```

</details>

Require the assignment and section 5 checks to be complete before this read.

<details>
<summary>Complete commands: current-month API check after section 5</summary>

```sh
set +e
set +u
set -o pipefail

acceptance_run_current_month_check() {
  (
    set -euo pipefail
    acceptance_current_month=$(date -u +%Y-%m)
    curl --fail-with-body "$acceptance_api/customers/cyberdyne/limit-status" \
      | jq -e --arg month "$acceptance_current_month" \
        '.month == $month and .pending_events == 0 and .processing_errors == 0'
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"
}

acceptance_run_current_month_check
acceptance_scenario_status=$?
printf 'Scenario exit status: %s\n' "$acceptance_scenario_status"
```

</details>

## 7. Persistence and immutable reads across restart

**Initial state**

- Sections 4 and 5 have passed in the assignment project, and their invoice and
  persisted-state evidence is retained.
- The same project, volumes, source, and workflow state are selected; the producer
  checkpoint is `completed=24 steps=24`.
- API and PostgreSQL are running, and no new billing actions or migration changes
  have been introduced since the section 5 checks.

| Step | Action and inputs | Expected result |
| --- | --- | --- |
| [7.1](#71-save-the-invoice-snapshots-before-restart) | Read all three invoices and save complete canonical JSON with `jq -S`. | Three `*-before.json` snapshots exist for comparison; formatting/key order cannot affect equality. |
| [7.2](#72-stop-and-restart-the-complete-assignment-environment) | Stop and start the same project without removing volumes; rerun migrations and read producer status. | Services become healthy; migration is a no-op; `completed=24 steps=24`. |
| [7.3](#73-read-the-invoices-again-and-compare-all-retained-state) | Save `*-after.json`, compare all three invoices, reread accounts, and repeat section 5's SQL queries. | Three `cmp` calls exit `0`; Acme balance `700_000_000` microcents/version 8; Cyberdyne October limit reached/version 4; all database counts, totals, ledger, frozen groups, and invoice numbers unchanged; no new migration/activity. |
| [7.4](#74-save-the-restart-result-and-reset-after-the-assignment-scenario) | Record assignment/restart results; retain snapshots, producer state, SQL output, comparisons, and service logs; reset. | Assignment activity and producer progress removed; original seed catalog and empty accounts restored; evidence available for section 9. |

### 7.1 Save the invoice snapshots before restart

Capture all three full JSON invoices. Use canonical JSON for equality so
formatting/key order does not affect the comparison:

```sh
(
  set -euo pipefail
  test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
  for acceptance_invoice in acme/2026-10 acme/2026-11 cyberdyne/2026-10; do
    acceptance_customer=${acceptance_invoice%/*}
    acceptance_month=${acceptance_invoice#*/}
    curl --fail-with-body \
      "$acceptance_api/customers/$acceptance_customer/invoices/$acceptance_month" \
      | jq -S . > "$acceptance_evidence/$acceptance_customer-$acceptance_month-before.json"
  done
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

### 7.2 Stop and restart the complete assignment environment

```sh
(
  set -euo pipefail
  make stop
  make up
  make migrate
  make simulate SCENARIO=billing-assignment ACTION=status
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

### 7.3 Read the invoices again and compare all retained state

```sh
(
  set -euo pipefail
  test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
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
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

Repeat section 5's SQL queries as required by row 7.3. This checks retained
volumes and graceful process restart; it does not simulate a power failure or
lost/corrupted disk.

### 7.4 Save the restart result and reset after the assignment scenario

Record the assignment and restart results in `acceptance-results.md`. Retain the
invoice snapshots, simulator state saved in step 4.7, SQL output, and restart
comparisons in the evidence directory. Save the service logs before resetting:

```sh
(
  set -euo pipefail
  test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
  docker compose logs --no-color > "$acceptance_evidence/assignment-services.log"
  test "${COMPOSE_PROJECT_NAME:-}" = e2b-acceptance-01 || {
    printf '%s\n' 'Wrong project: select e2b-acceptance-01 before continuing.' >&2
    exit 1
  }
  docker compose down --volumes
  make up SERVICE=postgres
  make migrate
  make up
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

Continue after section 6.3. This block repeats the section 5 SQL query after
restart and pauses before the reset. Compare the printed database state and
saved invoice files with the documented expectations, record the results,
then type PASS in the terminal to allow step 7.4.

<details>
<summary>Complete commands: restart verification and assignment reset</summary>

```sh
set +e
set +u
set -o pipefail

acceptance_run_restart_verification() {
  (
    set -euo pipefail
    test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
    for acceptance_invoice in acme/2026-10 acme/2026-11 cyberdyne/2026-10; do
      acceptance_customer=${acceptance_invoice%/*}
      acceptance_month=${acceptance_invoice#*/}
      curl --fail-with-body \
        "$acceptance_api/customers/$acceptance_customer/invoices/$acceptance_month" \
        | jq -S . > "$acceptance_evidence/$acceptance_customer-$acceptance_month-before.json"
    done
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  (
    set -euo pipefail
    make stop
    make up
    make migrate
    make simulate SCENARIO=billing-assignment ACTION=status
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  (
    set -euo pipefail
    test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
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
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  (
    set -euo pipefail
    test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
    docker compose exec -T postgres psql -X -U e2b -d e2b_billing \
      -v ON_ERROR_STOP=1 <<'SQL' | tee "$acceptance_evidence/restart-final-state.txt"
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
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  printf '%s\n' 'Verify and record the restart results from steps 7.1–7.3. Type PASS to continue, or any other value to stop.'
  read -r acceptance_review < /dev/tty || return 1
  if test "$acceptance_review" != PASS; then
    printf '%s\n' 'Stopped before reset; retain the current data for review.' >&2
    return 1
  fi

  (
    set -euo pipefail
    test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
    docker compose logs --no-color > "$acceptance_evidence/assignment-services.log"
    test "${COMPOSE_PROJECT_NAME:-}" = e2b-acceptance-01 || {
      printf '%s\n' 'Wrong project: select e2b-acceptance-01 before continuing.' >&2
      exit 1
    }
    docker compose down --volumes
    make up SERVICE=postgres
    make migrate
    make up
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"
}

acceptance_run_restart_verification
acceptance_scenario_status=$?
printf 'Scenario exit status: %s\n' "$acceptance_scenario_status"
```

</details>

## 8. Credit-covered gross spend and add-on exclusion

**Initial state**

- Section 7's restart checks have passed and their evidence is saved. Step 7.4
  has reset the project after the assignment scenario.
- `e2b-acceptance-01` has the original seed catalog, empty accounts, and no saved
  simulator progress.
- The shell session retains the evidence directory, API URL, and port exports.
  The restarted services use those same ports.
- October 2026 is a completed UTC month: the server clock has reached at least
  `2026-11-01T00:00:00Z` before the invoice request. HTTP cannot override that clock.

| Step | Action and inputs | Expected result |
| --- | --- | --- |
| [8.1](#81-verify-the-reset-environment-for-the-combined-credit-and-limit-case) | Confirm service health and the API clock; repeat section 3's seed checks as `credit-limit-seed.txt`. | Original seed catalog, empty accounts, no producer progress; October can close; assignment evidence retained. |
| [8.2](#82-execute-each-request-and-compare-its-response) | Execute the twelve API actions in the table below using Scalar or identical curl requests. | Each status/field matches before the next action; usage completes before invoice issuance. Credit-covered 16 USD gross reaches the 15 USD limit; add-on excluded; invoice total 20 USD; remaining credit 9 USD; raising the limit to 20 USD clears the signal. |
| [8.3](#83-verify-the-combined-case-in-postgresql) | Query monthly usage, credit entries, and invoice state; save independent SQL and full invoice evidence. | One gross usage entry, one grant, one debit; exact amounts match the persisted-state table below; record this scenario separately. |
| [8.4](#84-save-the-combined-result-and-reset-after-the-scenario) | Save results and service logs, then reset using step 1.5. | Original seed catalog and empty accounts restored; section 9 can review all saved evidence. |

### 8.1 Verify the reset environment for the combined credit and limit case

Step 7.4 has already restarted the project. Confirm service health, then repeat
section 3's seed checks. Save that query output as `credit-limit-seed.txt` so the
assignment's seed evidence is retained.

```sh
(
  set -euo pipefail
  test "${COMPOSE_PROJECT_NAME:-}" = e2b-acceptance-01 || {
    printf '%s\n' 'Wrong project: select e2b-acceptance-01 before continuing.' >&2
    exit 1
  }
  make ps
  curl --fail-with-body "$acceptance_api/healthz"
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

### 8.2 Execute each request and compare its response

Follow the table in order using Scalar at the API reference URL printed by
`make links`, or identical curl requests to `$acceptance_api`. Save each response
and status code. In Scalar, select the endpoint, enter its parameters, paste any
request JSON, and send. Complete the polling row before issuing the invoice.

| Step | Action and inputs | Expected result |
| --- | --- | --- |
| 1 | Read Acme's fresh credit using GET `/customers/acme/credit`. | HTTP `200`; credit 0, version 0, pending/errors 0. |
| 2 | Grant 25 USD using POST `/customers/acme/credits`, `{"idempotency_key":"covered-grant","amount_cents":2500}`. | HTTP `200`; one grant; balance `2_500_000_000` microcents. |
| 3 | Buy `concurrency_pack` using POST `/customers/acme/addons`, `{"idempotency_key":"covered-pack","addon_name":"concurrency_pack","purchased_at":"2026-10-05"}`. | HTTP `200`; monthly price 20 USD, start month `2026-10-01`. |
| 4 | Set a 15 USD limit using POST `/customers/acme/spend-limit`, `{"idempotency_key":"covered-limit","limit_cents":1500}`. | HTTP `200`; limit 1_500 cents. |
| 5 | Read GET `/customers/acme/months/2026-10/limit-status` before usage. | HTTP `200`; gross 0, `limit_reached=false`, pending/errors 0. The 20 USD add-on is excluded from gross usage. |
| 6 | Send the JSON below to POST `/usage/batches`: Acme `400_000_000` units for October 10, 12:00–13:00. | HTTP `202`; every unit is durably accepted. |
| 7 | Poll GET `/customers/acme/months/2026-10/limit-status` until accounting completes. | HTTP `200`; gross `1_600_000_000` microcents (16 USD), limit 15 USD, `limit_reached=true`, version 4, pending/errors 0. |
| 8 | Issue October using POST `/customers/acme/invoices`, `{"month":"2026-10"}`. | HTTP `200`; `ACME-0001`: usage 16 USD + add-on 20 USD − credit 16 USD = **20 USD**. Exact gross and used credit each `1_600_000_000` microcents. |
| 9 | Read GET `/customers/acme/credit` after closing. | HTTP `200`; 9 USD (`900_000_000` microcents) remain, version 5, pending/errors 0; no additional invoice debit. |
| 10 | Read GET `/customers/acme/months/2026-10/limit-status` after closing. | HTTP `200`; gross 16 USD, limit 15 USD, `limit_reached=true`, version 5, pending/errors 0; credit-covered usage still reaches the gross limit. |
| 11 | Raise the limit using POST `/customers/acme/spend-limit`, `{"idempotency_key":"covered-raised","limit_cents":2000}`. | HTTP `200`; limit 20 USD. |
| 12 | Read GET `/customers/acme/months/2026-10/limit-status` after raising the limit. | HTTP `200`; unchanged gross 16 USD, limit 20 USD, `limit_reached=false`, version 6, pending/errors 0. |

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

### 8.3 Verify the combined case in PostgreSQL

Use PostgreSQL as in section 5. Save the SQL output as
`credit-limit-final-state.txt` and the full invoice as `credit-limit-invoice.json`;
keep assignment evidence unchanged. Compare with these literal outcomes and
[the API action table](#82-execute-each-request-and-compare-its-response).

| Persisted record / check | Expected result |
| --- | --- |
| Monthly gross usage | One entry, `1_600_000_000` microcents (16 USD). |
| Credit ledger | One `2_500_000_000`-microcent grant and one `1_600_000_000`-microcent usage debit; no add-on or closing debit. |
| Remaining credit | `900_000_000` microcents (9 USD). |
| October invoice | `ACME-0001`; usage 1_600 cents + add-on 2_000 cents − credit 1_600 cents = 2_000 cents; exact gross and used credit each `1_600_000_000` microcents. |
| Final limit status | After raising the limit: gross 16 USD, limit 20 USD, `limit_reached=false`, version 6, pending/errors 0. |

### 8.4 Save the combined result and reset after the scenario

Record this scenario's result and evidence locations in `acceptance-results.md`.
Save its service logs, then reset using the same procedure as step 1.5:

```sh
(
  set -euo pipefail
  test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
  docker compose logs --no-color > "$acceptance_evidence/credit-limit-services.log"
  test "${COMPOSE_PROJECT_NAME:-}" = e2b-acceptance-01 || {
    printf '%s\n' 'Wrong project: select e2b-acceptance-01 before continuing.' >&2
    exit 1
  }
  docker compose down --volumes
  make up SERVICE=postgres
  make migrate
  make up
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

Continue after the assignment reset. The block checks the server date and
environment, then pauses. While it waits, execute the API requests in step
8.2 through Scalar or curl and save the evidence required by step 8.3. These
manual requests are required parts of the scenario. Type PASS only after
recording their results; the block then saves logs and resets the project.

<details>
<summary>Complete terminal commands: combined credit and limit scenario</summary>

```sh
set +e
set +u
set -o pipefail

acceptance_run_combined_case_checkpoints() {
  (
    set -euo pipefail
    LC_ALL=C
    acceptance_api_clock=$(docker compose exec -T api date -u +%Y-%m-%dT%H:%M:%SZ)
    if [[ "$acceptance_api_clock" < "2026-11-01T00:00:00Z" ]]; then
      printf 'NOT RUN: server UTC time %s is before 2026-11-01T00:00:00Z.\n' "$acceptance_api_clock" >&2
      exit 1
    fi
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  (
    set -euo pipefail
    test "${COMPOSE_PROJECT_NAME:-}" = e2b-acceptance-01 || {
      printf '%s\n' 'Wrong project: select e2b-acceptance-01 before continuing.' >&2
      exit 1
    }
    make ps
    curl --fail-with-body "$acceptance_api/healthz"
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  printf '%s\n' 'Complete the API requests and save the evidence required by steps 8.2–8.3. Type PASS to continue, or any other value to stop.'
  read -r acceptance_review < /dev/tty || return 1
  if test "$acceptance_review" != PASS; then
    printf '%s\n' 'Stopped before reset; retain the current data for review.' >&2
    return 1
  fi

  (
    set -euo pipefail
    test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
    docker compose logs --no-color > "$acceptance_evidence/credit-limit-services.log"
    test "${COMPOSE_PROJECT_NAME:-}" = e2b-acceptance-01 || {
      printf '%s\n' 'Wrong project: select e2b-acceptance-01 before continuing.' >&2
      exit 1
    }
    docker compose down --volumes
    make up SERVICE=postgres
    make migrate
    make up
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"
}

acceptance_run_combined_case_checkpoints
acceptance_scenario_status=$?
printf 'Scenario exit status: %s\n' "$acceptance_scenario_status"
```

</details>

## 9. Architecture review, limitations, and completion record

**Initial state**

- The revision under review and any working-tree changes are identified.
- Evidence from the attempted checks in sections 2–8 is retained, and unexecuted
  checks are identified so they can be recorded as **NOT RUN**.
- The acceptance project has been reset after each completed scenario. Review
  saved evidence; previous scenario data is no longer in its Docker volumes.
- The architecture, financial, and interface documents linked below are available
  for the same revision under review.

| Step | Action and inputs | Expected result |
| --- | --- | --- |
| [9.1](#91-review-the-architecture-and-interface-responsibilities) | Read the linked design documents and record the explanations listed in the review table. | Responsibilities, transaction boundaries, ownership, tool choices, workload assumptions, and scaling limits are explained. |
| [9.2](#92-record-the-implementation-limits) | Record each implementation boundary and follow-up in the limits table. | The result distinguishes implemented contracts from planned scheduling, segmentation, correction policies, and production scaling. |
| [9.3](#93-complete-the-requirement-checklist-and-retain-the-evidence) | Complete scenario and requirement results using retained evidence; display the record and stop services. | Every required check has evidence and **PASS**, **FAIL**, or **NOT RUN**; only explained expected failures count as passes; evidence remains outside Docker and the repository. |

### 9.1 Review the architecture and interface responsibilities

Review the [submission overview](../../architecture/submission-overview.md),
[selected architecture and workload assumptions](../../brainstorming/architecture-options.md),
[usage-to-invoice flow](../../architecture/usage-to-invoice.md),
[financial rules](../../architecture/accounting-rules.md), and
[interface ownership](../../architecture/submission-overview.md#interfaces-and-ownership).

| Review topic | Required explanation / evidence |
| --- | --- |
| Ingestion and accounting | Why the separate platform sends durable, idempotent increments into PostgreSQL; why asynchronous accounting uses customer locks and explicit commit boundaries. |
| Limit freshness and enforcement | Why the platform polls status; how pending/error counts affect freshness; why platform enforcement after observing the signal belongs to its own team. |
| Interface ownership | Platform usage/limit reads; E2B price/grant/closing operations; customer add-on/limit/balance/invoice actions. Roles are conceptual; authorization enforcement is not implemented. |
| Tools and scaling | Why Go, PostgreSQL, Compose, and the durable simulator were selected; how the design addresses about 50_000 customers with several through thousands of concurrent sandboxes and minute records. Record scaling proposals and remaining operational decisions. This procedure establishes no billion-record capacity or measured production throughput/freshness guarantee. |

### 9.2 Record the implementation limits

| Boundary | Implemented contract / remaining limitation |
| --- | --- |
| Receipt intervals | One UTC month and one applicable price version per receipt; unsupported crossings remain errors, with no invented allocation. Broader segmentation is a follow-up. |
| Issuance | Explicit API invocation; automatic calendar scheduling is a follow-up. |
| Financial policy | Month ordering, half-up grouping, and credit allocation order are implementation decisions; additional correction policies remain follow-ups. |
| Closing cutoff | Only accounting committed before acquiring the customer lock is included; pending/quarantined target-month input never waits inside publication. |
| Month order | First invoice cannot skip earlier known usage; later new invoices require a closed predecessor. |
| Processing after closing | Routes forward without changing issued invoices; can introduce separate rounding groups. |
| Production capacity | Scaling remains a follow-up; this acceptance run establishes no production capacity guarantee. |

### 9.3 Complete the requirement checklist and retain the evidence

Open the `acceptance-results.md` created in step 1.2. Update the numbered
scenario results and all twelve requirement rows using the evidence collected
above. Use **NOT RUN** with an explanation for every
unexecuted part, including live scenarios blocked by their date precondition.

| Completion check | Required record / outcome |
| --- | --- |
| Run identity | Commit, working-tree changes, audit timestamp, Compose project, commands, and exit results. |
| Evidence | Full-suite logs, completed scenario counts, seed/final SQL output, captured invoices, and restart comparisons. |
| Scenario and requirement results | **PASS**, **FAIL**, or **NOT RUN** for each row, with evidence location and any limitation needing review. |
| Injected failures | **PASS** only when saved state, unchanged financial effects, and successful recovery match the corresponding scenario table. |
| Clean acceptance | Every required scenario has evidence; no unexplained financial mismatch, pending valid event, or processing error remains. Unresolved failure or an unexecuted requirement prevents clean acceptance. |

Keep the saved evidence available for review. The acceptance project contains
only the fresh seed state after the final reset; stop it with `make down` when
finished. Evidence is outside the tracked repository and Docker volumes.

First perform the reviews in steps 9.1–9.2 and update the checklist in step
9.3 manually. The block below displays that record and stops the acceptance
services while retaining volumes and evidence.

<details>
<summary>Complete terminal commands: display the completion record and stop services</summary>

```sh
set +e
set +u
set -o pipefail

acceptance_run_completion_record() {
  (
    set -euo pipefail
    test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
    cat "$acceptance_evidence/acceptance-results.md"
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  (
    set -euo pipefail
    test "${COMPOSE_PROJECT_NAME:-}" = e2b-acceptance-01 || {
      printf '%s\n' 'Wrong project: select e2b-acceptance-01 before continuing.' >&2
      exit 1
    }
    make down
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"
}

acceptance_run_completion_record
acceptance_scenario_status=$?
printf 'Scenario exit status: %s\n' "$acceptance_scenario_status"
```

</details>

[Previous](assignment.md) · [Overview](../final-acceptance-testing.md) · [Next](../final-acceptance-testing.md)
