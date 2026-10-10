# Final acceptance test procedure

## Contents

Start with section 1. Each entry identifies the preparation, scenario instructions,
or review and states what it establishes.

- [Introduction](#introduction)
- [1. Initial setup and test preparation](#1-initial-setup-and-test-preparation) — verify prerequisites, prepare the evidence directory and checklist, review expected inputs, check invoice dates, and read the reset procedure used after each completed scenario.
- [2. Automated regression gate in an isolated project](#2-automated-regression-gate-in-an-isolated-project) — run the complete automated suite, including SQL integrity in both time zones.
- [3. Fresh assignment environment and seed inspection](#3-fresh-assignment-environment-and-seed-inspection) — initialize the manual test environment; verify service health, the served API specification, seed values, and empty accounts.
- [4. Walk through the complete assignment and transport failures](#4-walk-through-the-complete-assignment-and-transport-failures) — follow the public-API billing assignment and verify retries, unavailable API recovery, lost replies, and durable receipt while the worker is stopped.
- [5. Final invoices and independently persisted state](#5-final-invoices-and-independently-persisted-state) — compare complete invoice responses and database records with literal expected amounts, balances, numbering, and counts.
- [6. Remaining business, integrity, and concurrency scenarios](#6-remaining-business-integrity-and-concurrency-scenarios) — verify retries and conflicts, price and credit rules, limit states, interval boundaries, concurrency, rollback, recovery, and the current-month API response.
- [7. Persistence and immutable reads across restart](#7-persistence-and-immutable-reads-across-restart) — restart the assignment environment and verify unchanged invoices, balances, database state, and simulator progress.
- [8. Credit-covered gross spend and add-on exclusion](#8-credit-covered-gross-spend-and-add-on-exclusion) — follow the combined manual scenario; verify credit-covered usage reaches the gross spend limit while add-ons are excluded and all usage is billed.
- [9. Architecture review, limitations, and completion record](#9-architecture-review-limitations-and-completion-record) — review documented responsibilities and limits, then complete the requirement checklist with saved evidence.

## Introduction

Use this procedure to review one revision of the complete billing service. Run the
automated regression gate, walk through the assignment using the public API, and
compare its responses with persisted accounting state. Record evidence before
declaring a requirement passed. A successful HTTP request alone is insufficient.

Use the [API contract](/reference/),
[financial rules](../architecture/accounting-rules.md), and
[named public workflows](../simulator/billing-scenarios.md) for implementation
details. The original assignment is supplied separately.

### Conventions used in this guide

- All monetary expectations use the assignment's USD seed catalog.
- Calendar months are UTC.
- One cent is 1_000_000 ticks.
- Invoice amounts are integer cents; exact API amounts are decimal strings.
- Underscores in prose and tables group digits; copyable JSON and SQL retain
  their valid numeric syntax.

## 1. Initial setup and test preparation

This section prepares the environment and the record for one test run. Its five
steps use the same numbers and titles in this list and in the headings below:

- [1.1 Prerequisites: repository, tools, and Docker](#11-prerequisites-repository-tools-and-docker) — configure shell error handling, check the repository directory and required tools, and verify Docker availability.
- [1.2 Initial setup: evidence directory and results checklist](#12-initial-setup-evidence-directory-and-results-checklist) — create the evidence directory, record the Git revision, working-tree status, and UTC start time, and initialize every checklist result as **NOT RUN**.
- [1.3 Assignment inputs and acceptance criteria](#13-assignment-inputs-and-acceptance-criteria) — review the starting data, required scenarios, pass conditions, and implementation decisions.
- [1.4 Date prerequisites for invoice scenarios](#14-date-prerequisites-for-invoice-scenarios) — check when the server can issue the invoices required by each manual scenario.
- [1.5 Prerequisites: reset after each completed scenario](#15-prerequisites-reset-after-each-completed-scenario) — save the results, clear the test database and simulator state, and restore the seed catalog for the next scenario.

Complete steps 1.1–1.4 and read step 1.5 before starting the acceptance checks in
section 2. Keep the same shell session: later commands reuse its evidence directory,
project name, and simulator helper functions. At each later test checkpoint, compare the actual
result with the stated expectation, save the evidence, and continue only when
they match. Record an unexplained mismatch as **FAIL** and investigate before
advancing.

### 1.1 Prerequisites: repository, tools, and Docker

Open a terminal in the repository root. The following commands work in Bash or
Zsh; use your existing shell session.

Keep automatic exit and unset-variable errors disabled in the working shell.
The checks below use `set -euo pipefail` inside parentheses: a failure stops that
block and prints its exit status, while the terminal stays open. Continue only
when the exit status is `0` and the expected results match. Variables and helper
functions needed later are defined outside these parentheses.

```sh
set +e
set +u
set -o pipefail

(
  set -euo pipefail
  test -f compose.yaml || { printf '%s\n' 'Missing compose.yaml: open the repository root.' >&2; exit 1; }
  test -f Makefile || { printf '%s\n' 'Missing Makefile: open the repository root.' >&2; exit 1; }
  for acceptance_tool in git docker make curl jq cmp; do
    command -v "$acceptance_tool"
  done
  docker compose version
  docker info --format '{{.ServerVersion}}'
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

**Expected:** both files exist, every tool resolves to an executable, Docker
Compose prints its version, and the Docker daemon responds with its server
version. If a command fails, install or start the missing prerequisite before
continuing. Local Go is optional; the checks below use the pinned Docker toolchain.

### 1.2 Initial setup: evidence directory and results checklist

Create a directory outside the repository for this run's evidence:

```sh
acceptance_evidence=$(mktemp -d "${TMPDIR:-/tmp}/e2b-acceptance.XXXXXX")

(
  set -euo pipefail
  test -d "$acceptance_evidence"
  git rev-parse HEAD | tee "$acceptance_evidence/revision.txt"
  git status --short | tee "$acceptance_evidence/working-tree.txt"
  date -u +%Y-%m-%dT%H:%M:%SZ | tee "$acceptance_evidence/started-at.txt"
  cat > "$acceptance_evidence/acceptance-results.md" <<'RESULTS'
# Acceptance results

## Scenario results

Step numbers and titles match the acceptance guide. Record each scenario separately.
Optional diagnostic reruns in step 6.2 support the corresponding failed check.

| Guide step | Scenario / check | Result | Evidence / explanation |
| --- | --- | --- | --- |
| 2.2 | Apply migrations twice and compare their records | NOT RUN | |
| 2.3 | Run static checks and the complete regression suite | NOT RUN | |
| 2.4 | Run two migration runners concurrently | NOT RUN | |
| 3 | Fresh assignment environment and seed inspection | NOT RUN | |
| 4 | Walk through the complete assignment and transport failures | NOT RUN | |
| 5 | Final invoices and independently persisted state | NOT RUN | |
| 6.1 | Check that every required regression scenario actually ran | NOT RUN | |
| 6.3 | Verify the current-month public API response | NOT RUN | |
| 7 | Persistence and immutable reads across restart | NOT RUN | |
| 8 | Credit-covered gross spend and add-on exclusion | NOT RUN | |
| 9 | Architecture review, limitations, and completion record | NOT RUN | |

## Requirement coverage

A requirement can be checked in several scenarios; the guide steps below identify them.

| Guide steps | Requirement | Result | Evidence / explanation |
| --- | --- | --- | --- |
| 3.4 | Seed customers, addresses, metric, prices, and add-on | NOT RUN | |
| 4, 5.2, 6.1 | Sandbox measurement transfer | NOT RUN | |
| 4, 5, 6.1 | Historical prices and customer override | NOT RUN | |
| 4, 5.2, 6.1, 8 | Credit grant and remaining balance | NOT RUN | |
| 4, 5, 6.1, 8 | Credit covers usage only | NOT RUN | |
| 4, 5, 6.1, 8 | Full monthly add-on charge | NOT RUN | |
| 4, 5.2, 6.1, 6.3, 8 | Spend-limit configuration and reached status | NOT RUN | |
| 4, 5, 6.1, 8 | Gross-usage limit, add-on exclusion, continued accounting | NOT RUN | |
| 4, 5, 6.1, 7, 8 | Monthly invoice amounts and customer numbering | NOT RUN | |
| 4.6, 5, 6.1, 7 | Late usage and immutable issued invoices | NOT RUN | |
| 4, 6.1, 7 | Retries, delay, unavailable services, and restart | NOT RUN | |
| 9.1, 9.2 | Architecture, ownership, tools, limitations, and improvements | NOT RUN | |
RESULTS
  printf 'Evidence directory: %s\n' "$acceptance_evidence"
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

**Expected:** the directory contains the revision, working-tree status, UTC start
time, numbered scenario results, and twelve **NOT RUN** requirement rows.
Keep its printed path. If testing
local review edits, retain the working-tree record and describe those edits in
the results file; the commit hash alone does not identify an uncommitted revision.
Update each row to **PASS** or **FAIL** only after collecting its required evidence.
For example, record the migration comparison in scenario row **2.2**. The
requirement table lists the guide steps that establish each requirement.

To view the checklist, run this command in the same terminal where you completed
this step:

```sh
(
  set -euo pipefail
  test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
  cat "$acceptance_evidence/acceptance-results.md"
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

To resume in a new terminal, open the repository root and rerun step 1.1. Restore
the saved evidence directory below instead of repeating step 1.2. Enter the path
printed during setup. Step 2.1 restores the project and port exports; steps 3.1
and 4.1 restore the URLs and simulator helper when needed.

```sh
printf 'Saved evidence directory: '
read -r acceptance_evidence

(
  set -euo pipefail
  test -f "$acceptance_evidence/acceptance-results.md"
  printf 'Evidence directory: %s\n' "$acceptance_evidence"
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

### 1.3 Assignment inputs and acceptance criteria

Read the seed expectations and requirement matrix below. These are the literal
inputs and results to compare during later steps; do not change them to match an
unexpected result. This step itself sends no financial commands.

**Initial state**

- The binding seed catalog contains the two customers, Acme and Cyberdyne, with
  the identities and billing addresses checked in section 3.
- The usage metric is `cpu_seconds`.
- Default prices are USD 0.05 and USD 0.06 per million units, effective from
  1 and 15 October 2026 respectively.
- Acme's customer price is USD 0.04 per million units from 1 October 2026.
- The `concurrency_pack` catalog price is USD 20 per month.
- Both accounts start without credit or spend limits. Grants, purchases, limits,
  measurements, and invoices are actions performed during testing; they are not
  seeded activity.

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

### 1.4 Date prerequisites for invoice scenarios

New invoices require a completed UTC calendar month. The live assignment uses
October and November 2026, so its API server must have reached
`2026-12-01T00:00:00Z` to complete all 24 steps. HTTP inputs cannot override this
clock. The combined case in section 8 requires only October to have ended, so its
invoice can be issued from `2026-11-01T00:00:00Z`. Confirm the API container's UTC
time in step 3.2 before running the complete assignment workflow.

The automated invoice and simulator tests use explicit test-server clocks and
can run before these dates. Record each unexecuted manual check as **NOT RUN**
with its date precondition as the reason when that precondition prevents execution.

### 1.5 Prerequisites: reset after each completed scenario

Use one isolated acceptance project throughout this guide. After each completed
independent scenario, save its results and evidence outside Docker, then reset
the project before starting the next scenario. Git retains the source code;
Docker volumes hold the database and simulator progress.

Read these commands now; run them at the completion checkpoints in steps 2.5,
7.4, and 8.4, after selecting the acceptance project in step 2.1:

```sh
(
  set -euo pipefail
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

The block checks that the acceptance project is selected before deleting data.
If that check fails, no reset command runs. `down --volumes`
removes its containers, network, database, and saved simulator state. Starting
PostgreSQL and applying migrations restores the schema and seed catalog; `make up`
then starts all services. The evidence directory created in step 1.2 is retained.
Plain `make down` retains the volumes and does not reset a scenario.

Sections 3–7 form one assignment scenario: setup, execution, invoice and database
checks, the current-month API read in step 6.3, and the restart check. Reset only
after all of them are complete. Section 8 is a separate scenario. Apply the same
reset after any additional manual JSON workflow. Automated fixtures manage their
own isolation; reset after the complete regression gate and any diagnostic reruns.
If a check fails, retain its state for investigation and recovery before resetting.


The block below starts a new evidence record and displays its checklist.
Read steps 1.3–1.5 before proceeding; run the reset only at its documented
completion checkpoints.

<details>
<summary>Complete commands: initial setup</summary>

```sh
set +e
set +u
set -o pipefail

acceptance_run_initial_setup() {
  set +e
  set +u
  set -o pipefail

  (
    set -euo pipefail
    test -f compose.yaml || { printf '%s\n' 'Missing compose.yaml: open the repository root.' >&2; exit 1; }
    test -f Makefile || { printf '%s\n' 'Missing Makefile: open the repository root.' >&2; exit 1; }
    for acceptance_tool in git docker make curl jq cmp; do
      command -v "$acceptance_tool"
    done
    docker compose version
    docker info --format '{{.ServerVersion}}'
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  acceptance_evidence=$(mktemp -d "${TMPDIR:-/tmp}/e2b-acceptance.XXXXXX")

  (
    set -euo pipefail
    test -d "$acceptance_evidence"
    git rev-parse HEAD | tee "$acceptance_evidence/revision.txt"
    git status --short | tee "$acceptance_evidence/working-tree.txt"
    date -u +%Y-%m-%dT%H:%M:%SZ | tee "$acceptance_evidence/started-at.txt"
    cat > "$acceptance_evidence/acceptance-results.md" <<'RESULTS'
# Acceptance results

## Scenario results

Step numbers and titles match the acceptance guide. Record each scenario separately.
Optional diagnostic reruns in step 6.2 support the corresponding failed check.

| Guide step | Scenario / check | Result | Evidence / explanation |
| --- | --- | --- | --- |
| 2.2 | Apply migrations twice and compare their records | NOT RUN | |
| 2.3 | Run static checks and the complete regression suite | NOT RUN | |
| 2.4 | Run two migration runners concurrently | NOT RUN | |
| 3 | Fresh assignment environment and seed inspection | NOT RUN | |
| 4 | Walk through the complete assignment and transport failures | NOT RUN | |
| 5 | Final invoices and independently persisted state | NOT RUN | |
| 6.1 | Check that every required regression scenario actually ran | NOT RUN | |
| 6.3 | Verify the current-month public API response | NOT RUN | |
| 7 | Persistence and immutable reads across restart | NOT RUN | |
| 8 | Credit-covered gross spend and add-on exclusion | NOT RUN | |
| 9 | Architecture review, limitations, and completion record | NOT RUN | |

## Requirement coverage

A requirement can be checked in several scenarios; the guide steps below identify them.

| Guide steps | Requirement | Result | Evidence / explanation |
| --- | --- | --- | --- |
| 3.4 | Seed customers, addresses, metric, prices, and add-on | NOT RUN | |
| 4, 5.2, 6.1 | Sandbox measurement transfer | NOT RUN | |
| 4, 5, 6.1 | Historical prices and customer override | NOT RUN | |
| 4, 5.2, 6.1, 8 | Credit grant and remaining balance | NOT RUN | |
| 4, 5, 6.1, 8 | Credit covers usage only | NOT RUN | |
| 4, 5, 6.1, 8 | Full monthly add-on charge | NOT RUN | |
| 4, 5.2, 6.1, 6.3, 8 | Spend-limit configuration and reached status | NOT RUN | |
| 4, 5, 6.1, 8 | Gross-usage limit, add-on exclusion, continued accounting | NOT RUN | |
| 4, 5, 6.1, 7, 8 | Monthly invoice amounts and customer numbering | NOT RUN | |
| 4.6, 5, 6.1, 7 | Late usage and immutable issued invoices | NOT RUN | |
| 4, 6.1, 7 | Retries, delay, unavailable services, and restart | NOT RUN | |
| 9.1, 9.2 | Architecture, ownership, tools, limitations, and improvements | NOT RUN | |
RESULTS
    printf 'Evidence directory: %s\n' "$acceptance_evidence"
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  (
    set -euo pipefail
    test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
    cat "$acceptance_evidence/acceptance-results.md"
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"
}

acceptance_run_initial_setup
acceptance_scenario_status=$?
printf 'Scenario exit status: %s\n' "$acceptance_scenario_status"
```

</details>

## 2. Automated regression gate in an isolated project

**Initial state**

- For a new run, `e2b-acceptance-01` is unused and the ports exported below
  are free. Its volumes are separate from `e2b-local`.
- Isolated-schema billing workflows supply explicit server clocks, so their fixed
  invoice dates do not depend on the current date.

When recovering a failed run, retain its original project and state file. Changing
only the simulator's `SOURCE` or state filename does not reset balances or invoices.

### 2.1 Select an isolated project and start PostgreSQL

Continue in the shell session prepared in section 1. Use the dedicated project
name below and free ports; change the example port values if they are already in
use. If this project already contains a previous acceptance run, retain its
evidence and reset it using step 1.5 before starting a new run.

```sh
export COMPOSE_PROJECT_NAME=e2b-acceptance-01
export E2B_POSTGRES_PORT=25433 E2B_API_PORT=28081 E2B_DOCS_PORT=28082

(
  set -euo pipefail
  test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
  printf '%s\n' "$COMPOSE_PROJECT_NAME" >> "$acceptance_evidence/projects.txt"
  docker ps --format '{{.Names}}\t{{.Ports}}'
  docker compose config --services | tee "$acceptance_evidence/services.txt"
  make up SERVICE=postgres
  make ps SERVICE=postgres
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

**Expected:** the service list includes `postgres`, `api`, `worker`, `simulator`,
`docs`, and `api-docs`; PostgreSQL starts and is healthy. The project name and
ports are separate from the existing development environment.

If Docker reports `port is already allocated`, change the affected port export
to a free value and rerun this step. The container list above shows Docker's
current port assignments. Retain existing projects and their data.

### 2.2 Apply migrations twice and compare their records

```sh
(
  set -euo pipefail
  test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
  make migrate | tee "$acceptance_evidence/migrate-first.log"
  make migration-status > "$acceptance_evidence/migrations-first.txt"
  make migrate | tee "$acceptance_evidence/migrate-repeat.log"
  make migration-status | tee "$acceptance_evidence/migrations.txt"
  cmp "$acceptance_evidence/migrations-first.txt" "$acceptance_evidence/migrations.txt"
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

**Expected:** both migration runs exit zero. The migration table contains exactly
versions 1 through 9, each once. `cmp` exits zero without output: a repeat applies
no additional migration and preserves every `applied_at` value.


For a new migration comparison, the following block combines steps 1.1, 1.2,
2.1, and 2.2. Start in the repository root with free ports. It creates a new
evidence directory, stops at the first failed step, and keeps the terminal open.
It does not reset existing volumes. To resume an existing record, restore its
directory as described in step 1.2 and run steps 2.1–2.2 separately.

<details>
<summary>Complete commands: preparation and two migration runs</summary>

```sh
set +e
set +u
set -o pipefail

acceptance_run_migration_comparison() {
  set +e
  set +u
  set -o pipefail

  (
    set -euo pipefail
    test -f compose.yaml || { printf '%s\n' 'Missing compose.yaml: open the repository root.' >&2; exit 1; }
    test -f Makefile || { printf '%s\n' 'Missing Makefile: open the repository root.' >&2; exit 1; }
    for acceptance_tool in git docker make curl jq cmp; do
      command -v "$acceptance_tool"
    done
    docker compose version
    docker info --format '{{.ServerVersion}}'
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  acceptance_evidence=$(mktemp -d "${TMPDIR:-/tmp}/e2b-acceptance.XXXXXX")

  (
    set -euo pipefail
    test -d "$acceptance_evidence"
    git rev-parse HEAD | tee "$acceptance_evidence/revision.txt"
    git status --short | tee "$acceptance_evidence/working-tree.txt"
    date -u +%Y-%m-%dT%H:%M:%SZ | tee "$acceptance_evidence/started-at.txt"
    cat > "$acceptance_evidence/acceptance-results.md" <<'RESULTS'
# Acceptance results

## Scenario results

Step numbers and titles match the acceptance guide. Record each scenario separately.
Optional diagnostic reruns in step 6.2 support the corresponding failed check.

| Guide step | Scenario / check | Result | Evidence / explanation |
| --- | --- | --- | --- |
| 2.2 | Apply migrations twice and compare their records | NOT RUN | |
| 2.3 | Run static checks and the complete regression suite | NOT RUN | |
| 2.4 | Run two migration runners concurrently | NOT RUN | |
| 3 | Fresh assignment environment and seed inspection | NOT RUN | |
| 4 | Walk through the complete assignment and transport failures | NOT RUN | |
| 5 | Final invoices and independently persisted state | NOT RUN | |
| 6.1 | Check that every required regression scenario actually ran | NOT RUN | |
| 6.3 | Verify the current-month public API response | NOT RUN | |
| 7 | Persistence and immutable reads across restart | NOT RUN | |
| 8 | Credit-covered gross spend and add-on exclusion | NOT RUN | |
| 9 | Architecture review, limitations, and completion record | NOT RUN | |

## Requirement coverage

A requirement can be checked in several scenarios; the guide steps below identify them.

| Guide steps | Requirement | Result | Evidence / explanation |
| --- | --- | --- | --- |
| 3.4 | Seed customers, addresses, metric, prices, and add-on | NOT RUN | |
| 4, 5.2, 6.1 | Sandbox measurement transfer | NOT RUN | |
| 4, 5, 6.1 | Historical prices and customer override | NOT RUN | |
| 4, 5.2, 6.1, 8 | Credit grant and remaining balance | NOT RUN | |
| 4, 5, 6.1, 8 | Credit covers usage only | NOT RUN | |
| 4, 5, 6.1, 8 | Full monthly add-on charge | NOT RUN | |
| 4, 5.2, 6.1, 6.3, 8 | Spend-limit configuration and reached status | NOT RUN | |
| 4, 5, 6.1, 8 | Gross-usage limit, add-on exclusion, continued accounting | NOT RUN | |
| 4, 5, 6.1, 7, 8 | Monthly invoice amounts and customer numbering | NOT RUN | |
| 4.6, 5, 6.1, 7 | Late usage and immutable issued invoices | NOT RUN | |
| 4, 6.1, 7 | Retries, delay, unavailable services, and restart | NOT RUN | |
| 9.1, 9.2 | Architecture, ownership, tools, limitations, and improvements | NOT RUN | |
RESULTS
    printf 'Evidence directory: %s\n' "$acceptance_evidence"
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  export COMPOSE_PROJECT_NAME=e2b-acceptance-01
  export E2B_POSTGRES_PORT=25433 E2B_API_PORT=28081 E2B_DOCS_PORT=28082

  (
    set -euo pipefail
    test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
    printf '%s\n' "$COMPOSE_PROJECT_NAME" >> "$acceptance_evidence/projects.txt"
    docker ps --format '{{.Names}}\t{{.Ports}}'
    docker compose config --services | tee "$acceptance_evidence/services.txt"
    make up SERVICE=postgres
    make ps SERVICE=postgres
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  (
    set -euo pipefail
    test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
    make migrate | tee "$acceptance_evidence/migrate-first.log"
    make migration-status > "$acceptance_evidence/migrations-first.txt"
    make migrate | tee "$acceptance_evidence/migrate-repeat.log"
    make migration-status | tee "$acceptance_evidence/migrations.txt"
    cmp "$acceptance_evidence/migrations-first.txt" "$acceptance_evidence/migrations.txt"
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"
}

acceptance_run_migration_comparison
acceptance_scenario_status=$?
printf 'Scenario exit status: %s\n' "$acceptance_scenario_status"
```

</details>

### 2.3 Run static checks and the complete regression suite

```sh
(
  set -euo pipefail
  test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
  E2B_GO_CHECKS_DOCKER=1 make check 2>&1 | tee "$acceptance_evidence/check.log"
  make test 2>&1 | tee "$acceptance_evidence/test.log"
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

**Expected:** `make check` and `make test` both exit zero. Inspect `test.log` for
the named workflows and regression cases listed in section 6; record their
results before proceeding. The fresh catalog is checked by the SQL seed suite.

`make test` runs SQL integrity in **both UTC and Asia/Shanghai**, Go package tests,
isolated-schema persistence tests, HTTP tests against the running API, and worker
process lifecycle tests. Require the named billing scenarios in section 6 to
actually execute and pass; a filtered or skipped suite is not the full gate.
SQL fixtures roll back, isolated schemas are dropped, and HTTP tests clean their
owned receipts. Reset the project in step 2.5 before the manual assignment anyway.

### 2.4 Run two migration runners concurrently

Check migration-runner serialization against the same database:

```sh
(
  set -euo pipefail
  test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
  make migrate > "$acceptance_evidence/migrate-concurrent-1.log" 2>&1 &
  acceptance_migrate_one=$!
  make migrate > "$acceptance_evidence/migrate-concurrent-2.log" 2>&1 &
  acceptance_migrate_two=$!
  wait "$acceptance_migrate_one"
  wait "$acceptance_migrate_two"
  make migration-status > "$acceptance_evidence/migrations-concurrent.txt"
  cmp "$acceptance_evidence/migrations.txt" "$acceptance_evidence/migrations-concurrent.txt"
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

**Expected:** both runners exit zero, with the same nine version rows and unchanged seed
values. Retain both logs. This exercises concurrent no-op runners after a fresh
migration; it does not demonstrate a failing future migration or simultaneous
first-time application of future versions.

### 2.5 Retain the evidence and reset after the automated gate

The targeted commands in section 6 are diagnostic reruns in this test project;
the full gate already includes them. Finish those diagnostic reruns before starting
the walkthrough. Section 6's current-month API read runs after the assignment.
Save the gate's results and logs, then perform the reset described in step 1.5:

```sh
(
  set -euo pipefail
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

**Expected:** the same project is running with a freshly seeded database and no
saved simulator progress. Evidence files remain outside Docker. Check each manual
scenario's date prerequisites in step 1.4 before running it.


This new-run block includes setup, steps 2.1–2.5, and the final reset.
Use it with the starting state described above. Optional diagnostic filters
remain available in section 6 and must run before the reset.

<details>
<summary>Complete commands: preparation and automated regression gate</summary>

```sh
set +e
set +u
set -o pipefail

acceptance_run_regression_gate() {
  set +e
  set +u
  set -o pipefail

  (
    set -euo pipefail
    test -f compose.yaml || { printf '%s\n' 'Missing compose.yaml: open the repository root.' >&2; exit 1; }
    test -f Makefile || { printf '%s\n' 'Missing Makefile: open the repository root.' >&2; exit 1; }
    for acceptance_tool in git docker make curl jq cmp; do
      command -v "$acceptance_tool"
    done
    docker compose version
    docker info --format '{{.ServerVersion}}'
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  acceptance_evidence=$(mktemp -d "${TMPDIR:-/tmp}/e2b-acceptance.XXXXXX")

  (
    set -euo pipefail
    test -d "$acceptance_evidence"
    git rev-parse HEAD | tee "$acceptance_evidence/revision.txt"
    git status --short | tee "$acceptance_evidence/working-tree.txt"
    date -u +%Y-%m-%dT%H:%M:%SZ | tee "$acceptance_evidence/started-at.txt"
    cat > "$acceptance_evidence/acceptance-results.md" <<'RESULTS'
# Acceptance results

## Scenario results

Step numbers and titles match the acceptance guide. Record each scenario separately.
Optional diagnostic reruns in step 6.2 support the corresponding failed check.

| Guide step | Scenario / check | Result | Evidence / explanation |
| --- | --- | --- | --- |
| 2.2 | Apply migrations twice and compare their records | NOT RUN | |
| 2.3 | Run static checks and the complete regression suite | NOT RUN | |
| 2.4 | Run two migration runners concurrently | NOT RUN | |
| 3 | Fresh assignment environment and seed inspection | NOT RUN | |
| 4 | Walk through the complete assignment and transport failures | NOT RUN | |
| 5 | Final invoices and independently persisted state | NOT RUN | |
| 6.1 | Check that every required regression scenario actually ran | NOT RUN | |
| 6.3 | Verify the current-month public API response | NOT RUN | |
| 7 | Persistence and immutable reads across restart | NOT RUN | |
| 8 | Credit-covered gross spend and add-on exclusion | NOT RUN | |
| 9 | Architecture review, limitations, and completion record | NOT RUN | |

## Requirement coverage

A requirement can be checked in several scenarios; the guide steps below identify them.

| Guide steps | Requirement | Result | Evidence / explanation |
| --- | --- | --- | --- |
| 3.4 | Seed customers, addresses, metric, prices, and add-on | NOT RUN | |
| 4, 5.2, 6.1 | Sandbox measurement transfer | NOT RUN | |
| 4, 5, 6.1 | Historical prices and customer override | NOT RUN | |
| 4, 5.2, 6.1, 8 | Credit grant and remaining balance | NOT RUN | |
| 4, 5, 6.1, 8 | Credit covers usage only | NOT RUN | |
| 4, 5, 6.1, 8 | Full monthly add-on charge | NOT RUN | |
| 4, 5.2, 6.1, 6.3, 8 | Spend-limit configuration and reached status | NOT RUN | |
| 4, 5, 6.1, 8 | Gross-usage limit, add-on exclusion, continued accounting | NOT RUN | |
| 4, 5, 6.1, 7, 8 | Monthly invoice amounts and customer numbering | NOT RUN | |
| 4.6, 5, 6.1, 7 | Late usage and immutable issued invoices | NOT RUN | |
| 4, 6.1, 7 | Retries, delay, unavailable services, and restart | NOT RUN | |
| 9.1, 9.2 | Architecture, ownership, tools, limitations, and improvements | NOT RUN | |
RESULTS
    printf 'Evidence directory: %s\n' "$acceptance_evidence"
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  (
    set -euo pipefail
    test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
    cat "$acceptance_evidence/acceptance-results.md"
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  export COMPOSE_PROJECT_NAME=e2b-acceptance-01
  export E2B_POSTGRES_PORT=25433 E2B_API_PORT=28081 E2B_DOCS_PORT=28082

  (
    set -euo pipefail
    test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
    printf '%s\n' "$COMPOSE_PROJECT_NAME" >> "$acceptance_evidence/projects.txt"
    docker ps --format '{{.Names}}\t{{.Ports}}'
    docker compose config --services | tee "$acceptance_evidence/services.txt"
    make up SERVICE=postgres
    make ps SERVICE=postgres
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  (
    set -euo pipefail
    test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
    make migrate | tee "$acceptance_evidence/migrate-first.log"
    make migration-status > "$acceptance_evidence/migrations-first.txt"
    make migrate | tee "$acceptance_evidence/migrate-repeat.log"
    make migration-status | tee "$acceptance_evidence/migrations.txt"
    cmp "$acceptance_evidence/migrations-first.txt" "$acceptance_evidence/migrations.txt"
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  (
    set -euo pipefail
    test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
    E2B_GO_CHECKS_DOCKER=1 make check 2>&1 | tee "$acceptance_evidence/check.log"
    make test 2>&1 | tee "$acceptance_evidence/test.log"
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  (
    set -euo pipefail
    test -f "${acceptance_evidence:?Complete step 1.2 or restore its saved directory.}/acceptance-results.md"
    make migrate > "$acceptance_evidence/migrate-concurrent-1.log" 2>&1 &
    acceptance_migrate_one=$!
    make migrate > "$acceptance_evidence/migrate-concurrent-2.log" 2>&1 &
    acceptance_migrate_two=$!
    wait "$acceptance_migrate_one"
    wait "$acceptance_migrate_two"
    make migration-status > "$acceptance_evidence/migrations-concurrent.txt"
    cmp "$acceptance_evidence/migrations.txt" "$acceptance_evidence/migrations-concurrent.txt"
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
    docker compose down --volumes
    make up SERVICE=postgres
    make migrate
    make up
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"
}

acceptance_run_regression_gate
acceptance_scenario_status=$?
printf 'Scenario exit status: %s\n' "$acceptance_scenario_status"
```

</details>

## 3. Fresh assignment environment and seed inspection

**Initial state**

- Section 2's regression gate and any diagnostic reruns have finished, and the
  project has been reset in step 2.5.
- The same shell session retains `acceptance_evidence` and the port exports from
  section 2; the restarted services use those same ports.
- `e2b-acceptance-01` has a freshly seeded database and no simulator progress or
  customer activity from the automated gate.

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

**Expected:** healthy PostgreSQL, API, worker, docs, and api-docs; the simulator
runs idle. Health HTTP `200` verifies the API process. The seed query and financial
reads below verify database access.

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

**Expected:** the API container's UTC time is at least `2026-12-01T00:00:00Z`
before starting the complete assignment workflow. If this precondition is unmet,
leave that workflow **NOT RUN** with the date precondition as the reason. Future measurement
timestamps do not advance this clock; a premature invoice request returns `422`
without financial changes, although earlier workflow commands may have committed.

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

**Expected:** `cmp` exits zero without output. Open Scalar at the API reference
URL printed by `make ps` (`http://127.0.0.1:28082/reference/` with the example
ports), check its reference, and
use its embedded request client for `GET /customers/acme/credit`; expect HTTP `200`
and an empty account. Browser requests go through the same docs service's proxy.

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
- The server clock has reached `2026-12-01T00:00:00Z`, so both fixed October and
  November invoice months are completed UTC months. HTTP requests cannot override
  the clock. Before that date, use section 2's isolated-schema automated workflows.
- For a new run, `/state/acceptance-assignment.json` has no saved workflow progress.
  Recovery uses the same project, source, and state file at its saved checkpoint.

Use [billing-assignment.json](../simulator/billing-assignment.json) as the exact
request/response script. It has 24 named steps with literal financial expectations.
Each command below creates a new producer process while reusing its durable state.

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
a missing eligible catalog version. Use the [controlled SQL repair](../guides/accounting-recovery.md#controlled-historical-price-repair),
then explicitly release the receipt using the [recovery procedure](../guides/accounting-recovery.md#investigate-and-release-a-receipt).
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

The failed process must report a transport error and retain `completed=0 steps=24`.
No financial action occurred. The last two commands pass steps 1 and 2 against the
restarted API, with both accounts still zero. A failed assertion, invalid config,
or failed container build is not evidence of the expected network failure.

### 4.3 Verify the lost grant reply and complete steps 3–5

**Initial state**

- Steps 1 and 2 have passed (`completed=2`) after the API restart.
- Both accounts still have zero credit and version 0, with no pending events or
  processing errors. Acme's `welcome` grant has not yet been submitted.

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

Require `injected lost committed response`, with `completed=2 steps=24` despite the
successful grant commit. The credit read shows 2_500_000_000 ticks, version 1, and
zero pending/errors. The next process confirms the same `welcome` operation and
completes step 3 without another grant. The following commands purchase the pack
and set the limit, ending at `completed=5`. The workflow later loses an invoice
reply and a late-usage reply too; ordinary retries must preserve their results.

### 4.4 Stop the worker and verify durable receipt at step 6

**Initial state**

- Steps 3 through 5 have passed (`completed=5`); API and worker are running.
- Acme has 2_500_000_000 credit ticks, version 2, and the `acme-pack` subscription.
- Cyberdyne has zero credit, a 1_500-cent limit, and version 1.
- Neither customer has usage receipts, pending events, or processing errors.

```sh
(
  set -euo pipefail
  make stop SERVICE=worker
  acceptance_next
  curl --fail-with-body "$acceptance_api/customers/acme/credit" | jq .
  curl --fail-with-body \
    "$acceptance_api/customers/cyberdyne/months/2026-10/limit-status" | jq .
  make up SERVICE=worker
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

Step 6 returns durable HTTP `202`, with `completed=6`. Acme still has
2_500_000_000 ticks, version 2, one pending event, and zero errors. Cyberdyne has
zero gross ticks, version 1, one pending event, and zero errors. Both inbox rows
have `processed_at` and `processing_error` NULL, and no rating link yet. Read them
with the receipt query in section 5 if inspecting this checkpoint in SQL.
Restarting the worker must account for both already accepted increments.

### 4.5 Complete steps 7–9 and compare receipt metadata

**Initial state**

- Step 6 has passed (`completed=6`), and both first-hour receipts have been accepted.
- The worker has restarted. Steps 7 and 8 below await the resulting accounting
  state before the receipt snapshot and replay.

Complete the two accounting reads, snapshot the first receipts, and repeat them:

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

Require two processed rows before replay and an identical comparison afterward,
including their `received_at` and `processed_at`. The checkpoint is now 9.
The independent accounting tests also check unchanged financial effects.

### 4.6 Complete steps 10–24 against the checkpoint table

**Initial state**

- Steps 7 through 9 have passed (`completed=9`); both first-hour receipts are
  processed, and the saved receipt comparisons are identical.
- Acme has 2_100_000_000 credit ticks and version 3. Cyberdyne's October gross is
  617_283_945 ticks, below its 1_500-cent limit. Both have zero pending events and
  processing errors.
- All services needed for delivery and accounting are running in the same
  assignment project, using the same durable workflow state.

Run `acceptance_next` once for each remaining step 10 through 24, reviewing the
result against this table. Rows 1 through 9 describe the commands already run.
Financial commands return HTTP `200`; usage sends and retries return `202`.

For each remaining row, run this command once, compare the result with that row,
and only then run it again for the next step:

```sh
(
  set -euo pipefail
  acceptance_next
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

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

For an unattended replay on another fresh assignment project, one
`make simulate SCENARIO=billing-assignment MAX_ATTEMPTS=10` completes all 24 steps.
It does not reproduce the explicit stopped-service checkpoints above. Running it
on this completed state sends no further actions; a new state on used accounts
fails the fresh-account precondition.


Continue after the seed inspection. The block checks the server date before
sending financial commands. It expands step 4.6 into fifteen calls for steps
10–24. The simulator verifies each declared response; compare the printed
progress and saved evidence with the checkpoint table above.

<details>
<summary>Complete commands: all 24 assignment steps and transport failures</summary>

```sh
set +e
set +u
set -o pipefail

acceptance_run_assignment_walkthrough() {
  export SOURCE=acceptance-assignment STATE=/state/acceptance-assignment.json
  acceptance_next() {
    make simulate SCENARIO=billing-assignment MODE=step MAX_ATTEMPTS=10
  }

  (
    set -euo pipefail
    LC_ALL=C
    acceptance_api_clock=$(docker compose exec -T api date -u +%Y-%m-%dT%H:%M:%SZ)
    if [[ "$acceptance_api_clock" < "2026-12-01T00:00:00Z" ]]; then
      printf 'NOT RUN: server UTC time %s is before 2026-12-01T00:00:00Z.\n' "$acceptance_api_clock" >&2
      exit 1
    fi
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

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
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

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
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  (
    set -euo pipefail
    make stop SERVICE=worker
    acceptance_next
    curl --fail-with-body "$acceptance_api/customers/acme/credit" | jq .
    curl --fail-with-body \
      "$acceptance_api/customers/cyberdyne/months/2026-10/limit-status" | jq .
    make up SERVICE=worker
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

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
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

  (
    set -euo pipefail
    acceptance_next

    acceptance_next

    acceptance_next

    acceptance_next

    acceptance_next

    acceptance_next

    acceptance_next

    acceptance_next

    acceptance_next

    acceptance_next

    acceptance_next

    acceptance_next

    acceptance_next

    acceptance_next

    acceptance_next
  )
  acceptance_step_status=$?
  printf 'Step exit status: %s\n' "$acceptance_step_status"
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"

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
  test "$acceptance_step_status" -eq 0 || return "$acceptance_step_status"
}

acceptance_run_assignment_walkthrough
acceptance_scenario_status=$?
printf 'Scenario exit status: %s\n' "$acceptance_scenario_status"
```

</details>

## 5. Final invoices and independently persisted state

**Initial state**

- Section 4 has completed all 24 steps, with its response checks and saved producer
  checkpoint recorded in `acceptance_evidence`.
- The assignment project's API and PostgreSQL are running; both customers report
  zero pending events and processing errors.
- The three invoice responses and section 3's seed evidence are available for
  comparison. No additional billing actions have changed the assignment accounts.

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

**Expected:** all three reads return HTTP `200`. Open the saved JSON files and
compare all invoice fields with the seed buyer identity, `currency: USD`, explicit
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

Open `final-state.txt` and check every count, group, balance, and invoice below.
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

### 6.1 Check that every required regression scenario actually ran

Open the `test.log` saved in step 2.3. Find each scenario below and its passing
result. Record a missing or skipped scenario as **NOT RUN**; investigate a failed
scenario using step 6.2 in the automated project before continuing.

The full regression gate must also pass each case below.

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

Require `true`. If UTC rolls into a new month during the request, sample and read
again. The explicit November read above verifies the new-month business calculation
without changing clocks. Neither
case demonstrates an automatic invoice scheduler.


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

**Expected:** three complete invoice files are saved. Keep them for the comparison
in step 7.3.

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

**Expected:** the services become healthy, migration is a no-op, and the simulator
still reports `completed=24 steps=24`.

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

Require three successful comparisons, `completed=24 steps=24`, Acme's unchanged
700_000_000-tick balance/version 8, and Cyberdyne's unchanged reached October
status/version 4. Rerun the section 5 SQL queries: counts, totals, ledger, frozen
groups, and invoice numbers must remain identical. Migrations must add no new
version or activity. This checks retained volumes and graceful process restart;
it does not simulate a power failure or lost/corrupted disk.

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

**Expected:** assignment data and simulator progress are removed. The same project
is ready for section 8 with the original seed catalog and empty accounts. Saved
evidence remains available for the completion record in section 9.


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

This case combines two binding rules in one public-API workflow: an add-on alone
cannot reach a usage limit, and credit cannot reduce the gross usage compared with
that limit.

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

Verify the empty Acme account as in section 3. Submit these requests through
Scalar's embedded client at the API reference URL printed by `make links`, or send the identical JSON
with `curl` to `$acceptance_api`. Save responses and status codes in the evidence
directory. The customer path in every financial request is `/customers/acme`.
Follow the table from top to bottom. For each row, select the endpoint in Scalar,
enter its path parameters, paste the request JSON if present, and send the request.
Check its status and fields before moving to the next row. For the polling row,
repeat the GET until accounting finishes; do not issue the invoice beforehand.

| Step | Method/path and explicit input | Required result |
| --- | --- | --- |
| Grant | POST `/customers/acme/credits`, `{"idempotency_key":"covered-grant","amount_cents":2500,"recorded_at":"2026-10-01T00:00:00Z"}` | HTTP `200`, balance 2_500_000_000 ticks. |
| Purchase | POST `/customers/acme/addons`, `{"idempotency_key":"covered-pack","addon_name":"concurrency_pack","purchased_at":"2026-10-05T00:00:00Z"}` | HTTP `200`, monthly price 2_000 cents. |
| Limit | POST `/customers/acme/spend-limit`, `{"idempotency_key":"covered-limit","limit_cents":1500}` | HTTP `200`, limit 1_500 cents. |
| Before usage | GET `/customers/acme/months/2026-10/limit-status` | HTTP `200`, gross 0, reached `false`, pending/errors 0. The 2_000-cent pack exceeds the limit but is excluded. |
| Usage | POST `/usage/batches` with the JSON below | HTTP `202`; all 400_000_000 units are retained even though their gross charge exceeds the limit. |
| Await accounting | Poll GET `/customers/acme/months/2026-10/limit-status` | Gross 1_600_000_000 ticks, version 4, pending/errors 0. The worker must finish before the next step to assert this exact invoice. |
| Close | POST `/customers/acme/invoices`, `{"month":"2026-10"}` | HTTP `200`, `ACME-0001`, usage 1_600 cents + pack 2_000 − credit 1_600 = total 2_000. Exact gross and used credit are both 1_600_000_000 ticks. Closing reads the already processed group. |
| After closing | GET credit and October limit status | Balance 900_000_000 ticks, version 5; gross 1_600_000_000 ticks, limit 1_500 cents, reached `true`, pending/errors 0. Usage is fully credit-covered yet reaches the limit. |
| Raise | POST `/customers/acme/spend-limit`, `{"idempotency_key":"covered-raised","limit_cents":2000}`, then GET October status | HTTP `200`, unchanged gross 1_600_000_000 ticks, limit 2_000 cents, reached `false`, version 6. |

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

Inspect this project's `monthly_usage`, `credit_entries`, and invoice snapshot
through PostgreSQL as in section 5. Save the query output as
`credit-limit-final-state.txt` and the full invoice as `credit-limit-invoice.json`
in the evidence directory, keeping the assignment's evidence files unchanged.
Require one 1_600_000_000-tick gross usage entry, a
2_500_000_000-tick grant, one −1_600_000_000-tick debit, and the exact invoice above.
Record this case's PASS/FAIL separately.

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

**Expected:** the combined scenario's data is removed and the project contains
the original seed catalog and empty accounts. Section 9 reviews saved evidence.


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

### 9.1 Review the architecture and interface responsibilities

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

### 9.2 Record the implementation limits

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

### 9.3 Complete the requirement checklist and retain the evidence

Open the `acceptance-results.md` created in step 1.2. Update the numbered
scenario results and all twelve requirement rows using the evidence collected
above. Use **NOT RUN** with an explanation for every
unexecuted part, including live scenarios blocked by their date precondition.

Record the commit and any working-tree changes, date, Compose project name,
commands and exit results, full-suite logs, completed scenario counts, seed and
final SQL output, captured invoices, and restart comparisons. For each requirement
in section 1 mark **PASS**, **FAIL**, or **NOT RUN**, with an evidence location and
any limitation needing review. Expected failure injection is a pass only when its
specific saved state, unchanged financial effect, and successful recovery match.
The run is complete when every required scenario has evidence and no unexplained
financial mismatch, pending valid event, or processing error remains. An unresolved
failure or an unexecuted requirement prevents a clean acceptance result.

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
