# Acceptance: automated checks

Part 2 of 4 in the [acceptance procedure](../final-acceptance-testing.md).
Keep the same shell session, variables, helper functions, project selection,
and evidence directory throughout the procedure. Resets occur only at the
existing checkpoints 2.5, 7.4, and 8.4, after saving evidence.
Complete [Preparation](preparation.md) before continuing here.

## 2. Automated regression gate in an isolated project

**Initial state**

- For a new run, `e2b-acceptance-01` is unused and the ports exported below
  are free. Its volumes are separate from `e2b-local`.
- Isolated-schema billing workflows supply explicit server clocks, so their fixed
  invoice dates do not depend on the current date.

When recovering a failed run, retain its original project and state file. Changing
only the simulator's `SOURCE` or state filename does not reset balances or invoices.

| Step | Action and inputs | Expected result |
| --- | --- | --- |
| [2.1](#21-select-an-isolated-project-and-start-postgresql) | Select `e2b-acceptance-01`, export free ports, list services, and start PostgreSQL. | Service list includes `postgres`, `api`, `worker`, `simulator`, `docs`, and `api-docs`; PostgreSQL is healthy; development data stays isolated. |
| [2.2](#22-apply-migrations-twice-and-compare-their-records) | Apply migrations, save their records, repeat, and compare with `cmp`. | Both runs exit `0`; versions 1–13 appear exactly once; `cmp` exits `0`, preserving every `applied_at`. |
| [2.3](#23-run-static-checks-and-the-complete-regression-suite) | Run `make check` and unfiltered `make test`; retain logs. | Both exit `0`; named scenarios in section 6 execute and pass; SQL integrity passes in UTC and Asia/Shanghai. |
| [2.4](#24-run-two-migration-runners-concurrently) | Start two migration runners against the same migrated database; retain both logs and compare records. | Both exit `0`; the same thirteen version rows and seed values remain; migration records are identical. |
| [2.5](#25-retain-the-evidence-and-reset-after-the-automated-gate) | Finish any section 6 diagnostic reruns, save results, then reset using step 1.5. | Fresh seed database and no saved producer progress; evidence remains outside Docker. Check step 1.4 before each manual scenario. |

### 2.1 Select an isolated project and start PostgreSQL

Continue in the shell session prepared in section 1. Use the dedicated project
name below and free ports; change the example port values if they are already in
use. If this project already contains a previous acceptance run, retain its
evidence and reset it using step 1.5 before starting a new run.

The exported `COMPOSE_PROJECT_NAME` selects this project's containers, network,
database, and simulator volume for every subsequent `make` command. Port exports
only select host connections. Restore both project and port values in a new
terminal; an explicit `COMPOSE='docker compose -p ...'` override takes precedence
over the project export. See [Compose projects and ports](../local-development.md#compose-projects-and-ports)
and [simulator connectivity troubleshooting](../troubleshooting.md#troubleshoot-simulator-connectivity)
if the CLI reports `lookup api ... no such host`. `make simulate` does not start
missing dependent services.

```sh
export COMPOSE_PROJECT_NAME=e2b-acceptance-01
export E2B_POSTGRES_PORT=25433 E2B_API_PORT=28081 E2B_DOCS_PORT=28082
export E2B_DEMO_NOW=2026-10-31T00:00:00Z

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
export E2B_DEMO_NOW=2026-10-31T00:00:00Z

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
  E2B_DEMO_NOW= make test 2>&1 | tee "$acceptance_evidence/test.log"
)
acceptance_step_status=$?
printf 'Step exit status: %s\n' "$acceptance_step_status"
```

`make test` runs SQL integrity in **both UTC and Asia/Shanghai**, Go package tests,
isolated-schema persistence tests, HTTP tests against the running API, and worker
process lifecycle tests, and backend E2E. The ordinary API receipt tests use real
time (`E2B_DEMO_NOW=`); E2E supplies its own isolated clock phases. Require the named billing scenarios in section 6 to
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

This exercises concurrent no-op runners after a fresh
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
export E2B_DEMO_NOW=2026-10-31T00:00:00Z

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
    E2B_DEMO_NOW= make test 2>&1 | tee "$acceptance_evidence/test.log"
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

[Previous](preparation.md) · [Overview](../final-acceptance-testing.md) · [Next](assignment.md)
