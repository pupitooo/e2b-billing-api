# Acceptance: assignment walkthrough

Part 3 of 4 in the [acceptance procedure](../final-acceptance-testing.md).
Keep the same shell session, variables, helper functions, project selection,
and evidence directory throughout the procedure. Resets occur only at the
existing checkpoints 2.5, 7.4, and 8.4, after saving evidence.
Complete [Automated checks](automated-checks.md) before continuing here.

## 3. Fresh assignment environment and seed inspection

**Initial state**

- Section 2's regression gate and any diagnostic reruns have finished, and the
  project has been reset in step 2.5.
- The same shell session retains `acceptance_evidence` and the port exports from
  section 2; the restarted services use those same ports.
- `e2b-acceptance-01` has a freshly seeded database and no simulator progress or
  customer activity from the automated gate.

| Step | Action and inputs | Expected result |
| --- | --- | --- |
| [3.1](#31-confirm-the-reset-assignment-environment-is-running) | Confirm the project, inspect services, save API/docs URLs, and request `/healthz`. | Healthy PostgreSQL, API, worker, docs, and api-docs; idle simulator; health HTTP `200`. Seed and financial reads establish database access. |
| [3.2](#32-check-the-live-servers-utc-clock) | Save the API container's UTC time and compare with step 1.4. | Calendar and receipt-time prerequisites for the intended next step hold; otherwise record that manual check **NOT RUN**. Future usage timestamps do not advance the clock. |
| [3.3](#33-verify-the-served-specification-and-make-a-browser-request) | Fetch `/reference/openapi.yaml` and compare with the source; open Scalar and send `GET /customers/acme/credit`. | `cmp` exits `0`; browser request through the docs proxy returns HTTP `200` and an empty account. |
| [3.4](#34-query-and-compare-the-seed-data) | Query the catalog, accounts, and activity through a separate PostgreSQL connection. | Time zone `UTC`; every literal value in the seed table below matches; activity counts are zero. |

### 3.1 Confirm the reset assignment environment is running

Step 2.5 has already initialized and started the same project for the assignment.
Check its services and save the API and documentation URLs. Starting the idle
simulator must create no usage.

```sh
acceptance_api=http://127.0.0.1:$E2B_API_PORT
acceptance_docs=http://127.0.0.1:$E2B_DOCS_PORT

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

### 3.2 Check the live server's UTC clock

```sh
(
  set -euo pipefail
  test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
  docker compose exec -T api date -u +%Y-%m-%dT%H:%M:%SZ \
    | tee "$acceptance_evidence/api-clock.txt"
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

The clock check alone does not establish that the complete live assignment can
match its fixed late-usage results. Use step 1.4's calendar and receipt-time table;
earlier workflow commands may remain committed when a later precondition fails.

### 3.3 Verify the served specification and make a browser request

```sh
(
  set -euo pipefail
  test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
  curl --fail-with-body "$acceptance_docs/reference/openapi.yaml" \
    > "$acceptance_evidence/openapi-served.yaml"
  cmp docs/api/openapi.yaml "$acceptance_evidence/openapi-served.yaml"
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

Open Scalar at `http://127.0.0.1:28082/reference/` with the example ports and send
the browser request specified in row 3.3.

### 3.4 Query and compare the seed data

Inspect seed data through a separate database connection:

```sh
(
  set -euo pipefail
  test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
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
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
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
precondition. Investigate the mismatch, then reset using step 1.5 and repeat the
seed checks before starting the scenario. Keep the literal expectations unchanged.


Continue after step 2.5 in the same shell. Compare the service, clock,
specification, and seed results with this section before starting section 4.

<details>
<summary>Complete commands: assignment environment and seed inspection</summary>

```sh
set +e
set +u
set -o pipefail

acceptance_run_seed_inspection() {
  acceptance_api=http://127.0.0.1:$E2B_API_PORT
  acceptance_docs=http://127.0.0.1:$E2B_DOCS_PORT

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

  (
    set -euo pipefail
    test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
    docker compose exec -T api date -u +%Y-%m-%dT%H:%M:%SZ \
      | tee "$acceptance_evidence/api-clock.txt"
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  (
    set -euo pipefail
    test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
    curl --fail-with-body "$acceptance_docs/reference/openapi.yaml" \
      > "$acceptance_evidence/openapi-served.yaml"
    cmp docs/api/openapi.yaml "$acceptance_evidence/openapi-served.yaml"
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  (
    set -euo pipefail
    test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
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
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"
}

acceptance_run_seed_inspection
acceptance_scenario_status=$?
printf 'Scenario exit status: %s\n' "$acceptance_scenario_status"
```

</details>

## 4. Walk through the complete assignment and transport failures

**Initial state**

- Section 3's seed checks have passed in `e2b-acceptance-01`. Both
  accounts have zero credit, NULL limits, version 0, and next invoice number 1;
  there are no receipts, grants, subscriptions, or invoices.
- PostgreSQL, API, worker, docs, and api-docs are healthy; the simulator is idle.
  The shell session retains `acceptance_api`, `acceptance_evidence`, and port exports.
- The calendar and receipt-time prerequisites in step 1.4 hold at each relevant
  checkpoint. The live workflow must receive step 15 during November and wait
  until December for step 19 to match the fixed invoice expectations. Use section
  2's isolated-schema automated workflows for an uninterrupted verification.
- For a new run, `/state/acceptance-assignment.json` has no saved workflow progress.
  Recovery uses the same project, source, and state file at its saved checkpoint.

Use [billing-assignment.json](../../simulator/billing-assignment.json) as the exact
request/response script. It has 24 named steps with literal financial expectations.
Each command below creates a new producer process while reusing its durable state.

| Step | Action and inputs | Expected result |
| --- | --- | --- |
| [4.1](#41-configure-the-simulator-for-one-step-per-command) | Define `acceptance_next`; keep source `acceptance-assignment` and state `/state/acceptance-assignment.json`. | Each successful call completes one observed JSON step; `await` reads poll the worker's result. |
| [4.2](#42-stop-the-api-verify-failed-delivery-and-recover-steps-12) | Stop the API, attempt step 1 once, inspect the checkpoint, restart, and complete steps 1–2. | Transport failure leaves completed count 0 and empty accounts; recovery reaches `completed=2 steps=24`. |
| [4.3](#43-verify-the-lost-grant-reply-and-complete-steps-35) | Lose the committed 25 USD grant reply; inspect credit and progress; retry and complete purchase/limit commands. | One grant, one 20 USD subscription, Cyberdyne limit 15 USD; completed count 5. |
| [4.4](#44-stop-the-worker-and-verify-durable-receipt-at-step-6) | Stop the worker, send step 6, inspect accounts and inbox rows, then restart the worker. | HTTP `202`; completed count 6; one pending receipt per customer, no rating links or financial processing before restart. |
| [4.5](#45-complete-steps-79-and-compare-receipt-metadata) | Await accounting reads 7–8, snapshot the receipts, replay step 9, and compare metadata. | Acme credit 21 USD; Cyberdyne gross `617_283_945` ticks; completed count 9; identical receipt files and no extra financial effects. |
| [4.6](#46-complete-steps-1024-against-the-checkpoint-table) | Execute one command for each remaining row 10–24 in the linked canonical table, observing step 1.4's prerequisites. | Every declared HTTP response and literal amount matches; completed count 24; no pending events or processing errors. |
| [4.7](#47-save-and-verify-the-completed-simulator-checkpoint) | Save CLI status and the complete producer state. | `completed=24 steps=24`, `next_step=24`, `step_count=24`, and no `last_error`; both evidence files exist. |

### 4.1 Configure the simulator for one step per command

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
a missing eligible catalog version. Use the [controlled SQL repair](../accounting-recovery.md#controlled-historical-price-repair),
then explicitly release the receipt using the [recovery procedure](../accounting-recovery.md#investigate-and-release-a-receipt).
Ordinary `POST /prices` rejects new backdated versions; invoice issuance never
repairs or processes the receipt.
The assignment seed's effective starts remain unchanged; additional metrics must
be priced before their first measurement, and `effective_from` cannot be null.
Use explicit month paths for October/November; `/limit-status` uses the real
current UTC month. Future fixture timestamps do not advance the server clock.
A premature invoice request returns `422` without financial changes; earlier
workflow commands may already have committed.

### 4.2 Stop the API, verify failed delivery, and recover steps 1–2

**Initial state**

- No assignment step has completed (`completed=0`); both accounts are still in
  section 3's empty state.
- The API and worker are running before the API is stopped below.

| Step | Action and inputs | Expected result |
| --- | --- | --- |
| 1 | Stop the API and attempt assignment step 1 with `MAX_ATTEMPTS=1`. | Process fails with a transport error; `completed=0 steps=24`; no financial action occurred. |
| 2 | Read the saved workflow with `ACTION=status`. | Completed count stays 0; the failed-step error remains recorded. |
| 3 | Restart the API and execute the next two assignment steps. | Both fresh-account reads pass; accounts remain zero; `completed=2 steps=24`. |

```sh
(
  set -euo pipefail
  make stop SERVICE=api
  if make simulate SCENARIO=billing-assignment MODE=step MAX_ATTEMPTS=1; then
    printf '%s\n' 'FAIL: unavailable API unexpectedly completed a step' >&2
    exit 1
  fi
  make simulate SCENARIO=billing-assignment ACTION=status
  make up SERVICE=api
  acceptance_next
  acceptance_next
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```


A failed assertion, invalid configuration, or failed container build is not
evidence of the expected network failure.

### 4.3 Verify the lost grant reply and complete steps 3–5

**Initial state**

- Steps 1 and 2 have passed (`completed=2`) after the API restart.
- Both accounts still have zero credit and version 0, with no pending events or
  processing errors. Acme's `welcome` grant has not yet been submitted.

| Step | Action and inputs | Expected result |
| --- | --- | --- |
| 1 | Execute assignment step 3 with `MAX_ATTEMPTS=1`, losing its committed grant reply. | Process fails with `injected lost committed response`; `completed=2 steps=24`, although the 25 USD grant committed. |
| 2 | Read saved progress and Acme's credit independently. | Completed count 2; credit `2_500_000_000` ticks, version 1, pending/errors 0. |
| 3 | Resume the original step using `acceptance_next`. | The unchanged `welcome` command confirms the original grant; completed count 3, no extra credit. |
| 4 | Execute assignment step 4 using `acceptance_next`. | One `concurrency_pack` subscription at 20 USD per month; completed count 4. |
| 5 | Execute assignment step 5 using `acceptance_next`. | Cyberdyne limit 15 USD; completed count 5. |

```sh
(
  set -euo pipefail
  if make simulate SCENARIO=billing-assignment MODE=step MAX_ATTEMPTS=1; then
    printf '%s\n' 'FAIL: intentionally lost grant reply unexpectedly completed' >&2
    exit 1
  fi
  make simulate SCENARIO=billing-assignment ACTION=status
  curl --fail-with-body "$acceptance_api/customers/acme/credit" | jq .
  acceptance_next
  acceptance_next
  acceptance_next
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```


### 4.4 Stop the worker and verify durable receipt at step 6

**Initial state**

- Steps 3 through 5 have passed (`completed=5`); API and worker are running.
- Acme has 2_500_000_000 credit ticks, version 2, and the `acme-pack` subscription.
- Cyberdyne has zero credit, a 1_500-cent limit, and version 1.
- Neither customer has usage receipts, pending events, or processing errors.

| Step | Action and inputs | Expected result |
| --- | --- | --- |
| 1 | Stop the worker and execute assignment step 6. | HTTP `202`; completed count 6; both first-hour receipts are durable. |
| 2 | Read Acme's credit and Cyberdyne's October status while the worker is stopped. | Acme credit `2_500_000_000` ticks, version 2; Cyberdyne gross 0, version 1. Each customer has one pending event and zero errors. |
| 3 | Query the two inbox rows and their rating links before restarting the worker; save `receipts-worker-stopped.txt`. | Exactly two rows; both `processed_at` and `processing_error` are NULL; `rating_links=0` for each receipt. |
| 4 | Restart the worker, then proceed to the next accounting reads. | The existing receipts are processed; the canonical assignment's steps 7–8 establish the expected credit and gross amounts. |

```sh
(
  set -euo pipefail
  make stop SERVICE=worker
  acceptance_next
  curl --fail-with-body "$acceptance_api/customers/acme/credit" | jq .
  curl --fail-with-body \
    "$acceptance_api/customers/cyberdyne/months/2026-10/limit-status" | jq .
  docker compose exec -T postgres psql -X -U e2b -d e2b_billing \
    -v ON_ERROR_STOP=1 <<'SQL' | tee "$acceptance_evidence/receipts-worker-stopped.txt"
SELECT i.event_id, i.processed_at, i.processing_error,
       (SELECT count(*) FROM usage_ratings r
        WHERE r.source = i.source AND r.event_id = i.event_id) AS rating_links
FROM usage_inbox i
WHERE i.source = 'acceptance-assignment'
  AND i.event_id IN ('acme-first', 'cyberdyne-first')
ORDER BY i.event_id;
SQL
  make up SERVICE=worker
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```


### 4.5 Complete steps 7–9 and compare receipt metadata

**Initial state**

- Step 6 has passed (`completed=6`), and both first-hour receipts have been accepted.
- The worker has restarted. Steps 7 and 8 below await the resulting accounting
  state before the receipt snapshot and replay.

Complete the two accounting reads, snapshot the first receipts, and repeat them:

| Step | Action and inputs | Expected result |
| --- | --- | --- |
| 1 | Execute assignment steps 7–8 to await both accounts' processing. | Acme credit 21 USD; Cyberdyne October gross `617_283_945` ticks, below its 15 USD limit; pending/errors 0. |
| 2 | Save both first-hour inbox rows, including their receipt and processing timestamps. | Two processed rows saved in `receipts-before-retry.jsonl`. |
| 3 | Execute assignment step 9 to resend the same measurements. | HTTP `202`; completed count 9; no new receipt or financial effect. |
| 4 | Save the same rows again and compare the files with `cmp`. | Files identical, including `received_at` and `processed_at`; `cmp` exits zero. |

```sh
(
  set -euo pipefail
  test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
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
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```


### 4.6 Complete steps 10–24 against the checkpoint table

**Initial state**

- Steps 7 through 9 have passed (`completed=9`); both first-hour receipts are
  processed, and the saved receipt comparisons are identical.
- Acme has 2_100_000_000 credit ticks and version 3. Cyberdyne's October gross is
  617_283_945 ticks, below its 1_500-cent limit. Both have zero pending events and
  processing errors.
- All services needed for delivery and accounting are running in the same
  assignment project, using the same durable workflow state.

Use the canonical [24-step assignment table](../../simulator/billing-scenarios.md#billing-assignment).
Run the command below once per remaining row 10–24, comparing its result before
continuing. Rows 1–9 are already complete; sections 4.2–4.5 add the operator fault checks.

```sh
(
  set -euo pipefail
  acceptance_next
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

### 4.7 Save and verify the completed simulator checkpoint

Record the completed checkpoint and retain the producer state:

```sh
(
  set -euo pipefail
  test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
  make simulate SCENARIO=billing-assignment ACTION=status \
    | tee "$acceptance_evidence/assignment-status.txt"
  docker compose exec -T simulator cat /state/acceptance-assignment.json \
    > "$acceptance_evidence/assignment-state.json"
  jq -e '.next_step == 24 and .step_count == 24 and (.last_error // "") == ""' \
    "$acceptance_evidence/assignment-state.json"
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

An unattended `make simulate SCENARIO=billing-assignment MAX_ATTEMPTS=10` still
requires step 1.4's calendar and receipt times and does not reproduce the operator
fault checks. Running it on this completed state sends no further actions; a new
state on used accounts fails the fresh-account precondition.


Use the numbered commands above for the live walkthrough, preserving the same
project and checkpoint across the calendar boundaries in step 1.4. The complete
assignment is verified without those live-calendar waits by section 2's automated
fixtures; a combined live command block cannot satisfy both receipt and closing
times in one uninterrupted run.

## 5. Final invoices and independently persisted state

**Initial state**

- Section 4 has completed all 24 steps, with its response checks and saved producer
  checkpoint recorded in `acceptance_evidence`.
- The assignment project's API and PostgreSQL are running; both customers report
  zero pending events and processing errors.
- The three invoice responses and section 3's seed evidence are available for
  comparison. No additional billing actions have changed the assignment accounts.

| Step | Action and inputs | Expected result |
| --- | --- | --- |
| [5.1](#51-compare-the-complete-invoice-responses) | Read Acme October/November and Cyberdyne October invoices; save full JSON and compare fields and ordered lines. | Three HTTP `200` responses; identities, currency, months, numbers, exact ticks, and amounts match the tables below; line amounts sum to totals. |
| [5.2](#52-query-the-final-database-state-independently) | Query receipts, rating links/groups, monthly usage, account state, ledger, subscriptions, invoices, and closed months through PostgreSQL. | Independent output is saved in `final-state.txt`; expected values come from the literal tables, not production calculations. |
| [5.3](#53-compare-the-persisted-state-with-the-literal-expectations) | Compare every SQL record with the group and persisted-state tables below. | Six processed receipts, six rating links, five frozen groups, three invoices/closed months, and all literal financial values match. |

### 5.1 Compare the complete invoice responses

Read each invoice and save its complete response:

```sh
(
  set -euo pipefail
  test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
  for acceptance_invoice in acme/2026-10 acme/2026-11 cyberdyne/2026-10; do
    acceptance_customer=${acceptance_invoice%/*}
    acceptance_month=${acceptance_invoice#*/}
    curl --fail-with-body \
      "$acceptance_api/customers/$acceptance_customer/invoices/$acceptance_month" \
      | jq -S . > "$acceptance_evidence/$acceptance_customer-$acceptance_month-final.json"
  done
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

Compare the saved JSON with these literal lines and the field checks below:

| Invoice | Ordered line amounts, cents | Gross usage ticks | Credit used ticks | Total cents |
| --- | --- | --- | --- | --- |
| Acme October, `ACME-0001` | Usage 1_200; pack 2_000; credit −1_200 | 1_200_000_000 | 1_200_000_000 | 2_000 |
| Cyberdyne October, `CYBERDYNE-0001` | Usage at 5 cents: 617; at 6 cents: 1_200; credit 0 | 1_817_283_945 | 0 | 1_817 |
| Acme November, `ACME-0002` | Late October usage 200; November usage 400; pack 2_000; credit −600 | 600_000_000 | 600_000_000 | 2_000 |

| Invoice field / check | Expected result |
| --- | --- |
| Buyer | Customer identity and billing address match the section 3 seed table. |
| Currency, month, number, and exact audit ticks | `USD`, requested month, customer-specific number, and gross/credit ticks match the invoice table. |
| Cyberdyne's first usage charge | Exact USD 6.172_839_45 before presentation rounding; displayed amount 617 cents. |
| Acme November usage months | Late line `usage_month: 2026-10`; ordinary line `usage_month: 2026-11`. |
| Acme's late-line label | `Late usage from 2026-10` |
| Line sum | Every invoice's displayed line amounts sum exactly to its total. |
| Cyberdyne credit line | Explicit zero-cent line, even with no credit used. |

### 5.2 Query the final database state independently

Use a separate PostgreSQL session to inspect the records, rather than deriving
expected values using production accounting functions:

```sh
(
  set -euo pipefail
  test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
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
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

### 5.3 Compare the persisted state with the literal expectations

Compare `final-state.txt` with both tables below.

| Customer / price | Usage month → invoice month | Units | Gross ticks | Credit ticks | Booked gross cents |
| --- | --- | --- | --- | --- | --- |
| Acme / `cpu-acme-2026-10-01` | October → October | 300_000_000 | 1_200_000_000 | 1_200_000_000 | 1_200 |
| Acme / `cpu-acme-2026-10-01` | October → November | 50_000_000 | 200_000_000 | 200_000_000 | 200 |
| Acme / `cpu-acme-2026-10-01` | November → November | 100_000_000 | 400_000_000 | 400_000_000 | 400 |
| Cyberdyne / `cpu-default-2026-10-01` | October → October | 123_456_789 | 617_283_945 | 0 | 617 |
| Cyberdyne / `cpu-default-2026-10-15` | October → October | 200_000_000 | 1_200_000_000 | 0 | 1_200 |

| Persisted record / check | Expected result |
| --- | --- |
| Receipts and rating links | Six receipts and six links: exactly one each for `acme-first`, `acme-second`, `acme-late`, `acme-november`, `cyberdyne-first`, and `cyberdyne-second`. All processed; no errors. |
| Retry metadata | Original `received_at` and `processed_at` survive identical retries; step 4.5's files compare equal. |
| Acme monthly gross | October `1_400_000_000` ticks including late usage; November `400_000_000` ticks. Consumption-month gross can differ from invoice gross. |
| Cyberdyne monthly gross | October `1_817_283_945` ticks; November row absent, with the API projecting zero. |
| Acme account and ledger | Balance and ledger each `700_000_000` ticks; NULL limit; state version 8; next invoice number 3. |
| Cyberdyne account and ledger | Balance and ledger each 0; limit 1_500 cents; state version 4; next invoice number 2. |
| Subscriptions | One Acme `concurrency_pack` subscription. |
| Invoices, closed months, and frozen groups | Three invoices, three closed months, five frozen groups; invoice numbers and buyer details match the invoice/seed tables. |
| Credit entries | Five entries: one `2_500_000_000`-tick grant; four usage debits of `400_000_000`, `800_000_000`, `200_000_000`, and `400_000_000` ticks. |
| Add-on and closing effects on credit | No credit entry pays an add-on or is created by invoice issuance. |


Continue after the completed assignment checkpoint. Compare the invoice files
and query results with the literal expectations above before continuing.

<details>
<summary>Complete commands: final invoices and database state</summary>

```sh
set +e
set +u
set -o pipefail

acceptance_run_final_state_checks() {
  (
    set -euo pipefail
    test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
    for acceptance_invoice in acme/2026-10 acme/2026-11 cyberdyne/2026-10; do
      acceptance_customer=${acceptance_invoice%/*}
      acceptance_month=${acceptance_invoice#*/}
      curl --fail-with-body \
        "$acceptance_api/customers/$acceptance_customer/invoices/$acceptance_month" \
        | jq -S . > "$acceptance_evidence/$acceptance_customer-$acceptance_month-final.json"
    done
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  (
    set -euo pipefail
    test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
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
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"
}

acceptance_run_final_state_checks
acceptance_scenario_status=$?
printf 'Scenario exit status: %s\n' "$acceptance_scenario_status"
```

</details>

[Previous](automated-checks.md) · [Overview](../final-acceptance-testing.md) · [Next](final-checks.md)
