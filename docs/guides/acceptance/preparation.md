# Acceptance: preparation

Part 1 of 4 in the [acceptance procedure](../final-acceptance-testing.md).
Keep the same shell session, variables, helper functions, project selection,
and evidence directory throughout the procedure. Resets occur only at the
existing checkpoints 2.5, 7.4, and 8.4, after saving evidence.
Prepare the shared session here before running the following parts.

## 1. Initial setup and test preparation

| Step | Action and inputs | Expected result |
| --- | --- | --- |
| [1.1](#11-prerequisites-repository-tools-and-docker) | Open the repository root in Bash or Zsh; check `git`, `docker`, `make`, `curl`, `jq`, `cmp`, Compose, and the Docker daemon. | `compose.yaml` and `Makefile` exist; every tool resolves; Compose and Docker print their versions; block exit status `0`. Local Go is optional. |
| [1.2](#12-initial-setup-evidence-directory-and-results-checklist) | Create an evidence directory outside the repository; save revision, working-tree status, UTC start time, and checklist. | Evidence files exist; scenario rows and all twelve requirement rows start at **NOT RUN**. Retain the printed directory path. |
| [1.3](#13-assignment-inputs-and-acceptance-criteria) | Review the literal seed values and binding requirement matrix. | Inputs and pass conditions are known; no financial command is sent. |
| [1.4](#14-date-prerequisites-for-invoice-scenarios) | Prepare the fixed business-clock phases for the intended workflow. | All required phases are explicit; changing configuration preserves database and producer state. |
| [1.5](#15-prerequisites-reset-after-each-completed-scenario) | Read the reset procedure now; execute it only at checkpoints 2.5, 7.4, and 8.4 after saving evidence. | Only the selected acceptance project's data and producer state are removed; schema and seed catalog are restored; external evidence is retained. |

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

If a prerequisite check fails, install or start the missing tool before continuing.
The automated checks use the pinned Docker toolchain.

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

If testing local review edits, retain the working-tree record and describe those edits in
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

| Initial input | Required value |
| --- | --- |
| Customers | Acme and Cyberdyne, with identities and billing addresses checked in section 3. |
| Metric | `cpu_seconds` |
| Default prices | USD 0.05 per million from `2026-10-01`; USD 0.06 per million from `2026-10-15`. |
| Acme override | USD 0.04 per million from `2026-10-01`. |
| Add-on | `concurrency_pack`, USD 20 per month. |
| Accounts | No credit or spend limits; no grants, purchases, measurements, or invoices. Testing creates this activity. |

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
| Correctness through retries, delay, and unavailability | Sections 4 and 7 verify failed delivery, lost committed replies, durable storage in usage_inbox, immutable replay, and retained data after restart. Regression tests cover concurrent writers and transaction rollback. |
| Explain architecture, responsibilities, tools, unfinished work, and improvements | Section 9 reviews the delivered documentation and records remaining limitations. |

### 1.4 Date prerequisites for invoice scenarios

Use one trusted fixed business clock for API and worker in the presentation
project. No waiting for the host calendar is needed. HTTP timestamps never set
arrival time or the server's clock. Export `E2B_DEMO_NOW=2026-10-31T00:00:00Z`
before initial startup and after each independent-scenario reset.

| Scenario action | Business-clock phase | Expected result |
| --- | --- | --- |
| Assignment steps 1–12 | `2026-10-31T00:00:00Z` | Initial usage is accounted before closing. |
| Assignment steps 13–18 | `2026-11-01T00:00:00Z` | October closes; late October usage physically arrives in November and routes there. |
| Assignment steps 19–24 | `2026-12-01T00:00:00Z` | November closes; earlier snapshots and arrival times remain unchanged. |
| Combined credit/limit scenario | `2026-11-01T00:00:00Z` | October can close with its literal USD 20 add-on invoice. |
| Price versions | October before insertion, November accounting, December rejection/closing | See the [manual clock table](../e2e-walkthrough.md#other-public-fixtures). |

Define the following phase helper in the same shell. It checks the selected
project and pending usage_inbox rows, stops both business processes and recreates them
with the new environment. Database and producer volumes remain intact.

```sh
acceptance_advance() {
  (
    set -euo pipefail
    test "${COMPOSE_PROJECT_NAME:-}" = e2b-acceptance-01
    acceptance_pending=$(docker compose exec -T postgres psql -X -At -U e2b -d e2b_billing \
      -v ON_ERROR_STOP=1 -c 'SELECT count(*) FROM usage_inbox WHERE processed_at IS NULL AND processing_error IS NULL')
    test "$acceptance_pending" = 0
    docker compose stop api worker
    E2B_DEMO_NOW="$1" make up SERVICE=api
    E2B_DEMO_NOW="$1" make up SERVICE=worker
    E2B_DEMO_NOW="$1" make up SERVICE=docs
  ) || return $?
  export E2B_DEMO_NOW="$1"
  docker compose exec -T api printenv E2B_DEMO_NOW
  docker compose exec -T worker printenv E2B_DEMO_NOW
}
```

Both printed timestamps must match. Wait for ordinary accounting before a phase
change; investigate errors without moving the clock. Runtime deadlines, logs and
OS `date` retain real time. The [backend E2E suite](../e2e-testing.md) performs the
same transitions automatically. Human review can still record FAIL/NOT RUN for
an unmet setup or failed observation, but the runtime calendar is no obstacle.

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
  export E2B_DEMO_NOW=2026-10-31T00:00:00Z
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

[Previous](../final-acceptance-testing.md) · [Overview](../final-acceptance-testing.md) · [Next](automated-checks.md)
