# Test quality and mutation audit

A passing suite does not establish that it would detect a wrong implementation.
This focused audit deliberately changed one production rule at a time, ran the
relevant tests, restored the rule, and added regression scenarios for meaningful
survivors. Production behavior and migrations remain unchanged.

## Evidence and scope

Audit date: 10 October 2026. Go and SQL baseline:
`8f5d601fff8f90ce800dd584eab5bcd7ec656327` (merged PR #65).
The completed local GUI was inspected separately while it remained uncommitted
for review. Its source and test hashes are recorded in the
[mutation evidence](test-mutation-audit.json), together with every exact edit,
before/after result, killing test name, SQL error and equivalent-mutant rationale.

| Layer | Distinct mutations | Before regression additions | After regression additions |
| --- | --- | --- | --- |
| Go | 114 | 69 assertion failures, 6 suite timeouts, 39 survivors | 108 assertion failures; 6 equivalent survivors |
| SQL constraints | 24 | 20 detected; 4 survived every SQL file | All 24 detected in both UTC and Asia/Shanghai |
| GUI modules | 22 | 12 detected; 10 survived | All 22 detected |

These are 160 distinct mutations, not 160 tests. Each SQL mutation ran in both
time zones, giving 48 SQL mutation executions per pass. Three initial Go
timeouts already had failed assertions before the suite deadline; three worker
validation defects depended on the suite deadline alone. The final Go pass
produced direct assertion failures for all 108 behavior-changing mutations.
Uncompilable or ill-typed trial variants were repaired with
semantically equivalent edits before their results were included. Compiler
failures are not counted as successful defect detection.

The Go pass used owning package tests, plus real PostgreSQL repository scenarios
for persistence changes. Initial integration mutations ran the full inbox suite;
final integration reruns selected the owning workflow and stopped at the first
failure. A survivor of an owning unit suite can still be detected by an outer
integration suite; this audit strengthens the responsible layer without claiming
all original survivors escaped every possible test command.

This is a selected sample, concentrated on money, calendar boundaries, input
validation, transport acknowledgements, checkpoints, usage retries, and database
invariants. It does not measure a repository-wide mutation score. It does not
prove the absence of bugs, disk-loss durability, production performance, or full
browser correctness. `demo/app.mjs`, executables' configuration, all possible SQL
constraints and every concurrency interleaving were not exhaustively mutated.

## What needed improvement

| Responsibility | Demonstrated gap | Added verification |
| --- | --- | --- |
| Immutable money | Replacing `Ticks()`'s defensive copy with a pointer survived. Setting the returned integer to zero replaces its slice header and can miss shared nonzero storage. | Overwrite it with one and check both the original amount and a value copy. |
| Calendar routing | Routing late November usage into December of year 9999 had no adjacent valid test. | Literal December result in the domain and a real processor scenario checking every financial projection; existing exhaustion/quarantine cases remain. |
| Financial validation | Direct tests covered identifier size but omitted malformed Unicode, blank/NUL identifiers, timestamp precision and zero-versus-positive cents. | Exact validation fields/messages, valid offset/year neighbors, zero price/limit and rejected zero grant. |
| Invoice addition | Rejecting the valid maximum sum could pass, as could rejecting the valid negative minimum in the signed helper. | Both inclusive limits and their immediately overflowing neighbors. |
| Usage HTTP | Exact batch identifier size and Unicode surrogate endpoints were missing. | ASCII/Unicode 256-byte acceptance, valid endpoint pairs, and rejected unpaired endpoint escapes. |
| Simulator transport | Some configuration endpoints, HTTP 500, non-JSON acknowledgements, a single duplicate copy and exact body/file/response limits were missing. | Named direct scenarios, literal byte counts and transmission counts; the exact one-MiB batch uses valid identifiers. |
| Worker validation | Missing guards could hang invalid-configuration cases until the package timeout. | Local cancellation budgets preserve processor/heartbeat checks and yield immediate test failures for the same mutations. |
| SQL integrity | Removing positive schema-version, positive invoice-sequence, nonnegative invoice-total or object-snapshot checks survived all SQL files. | Named SQL with expected `23514`, plus adjacent valid values and rolled-back fixtures. |
| GUI | The last safe integer, zero grant, equal usage endpoints, schema version, extra invoice lines, complete captured snapshots, journal preservation, path restrictions and Retry-After were not all asserted. | Extend existing operation tests and add direct expectation/snapshot scenarios; 86 frontend tests pass locally. |

Go accounting unit coverage was already 97.4% before this audit, yet the mutable
money-storage mutation survived. Reaching a statement and asserting its contract
are separate properties. No production bug is claimed here: the deliberate
faults demonstrate weaknesses in test inputs or assertions.

## GUI review boundary

The GUI itself is absent from the committed baseline and remains editable on
local `main`. Its 18 additional test cases (including named operation groups)
have been applied to the local tests and verified there. The
[GUI regression patch](gui-test-regressions.patch) records those changes without
publishing the unrelated GUI implementation in this audit PR. Carry these test
changes into the GUI's own commit when that implementation is approved.

For a fresh copy containing the same completed GUI, apply the patch once:

```sh
git apply --unidiff-zero --check docs/guides/gui-test-regressions.patch
git apply --unidiff-zero docs/guides/gui-test-regressions.patch
make demo-check
```

The patch must not be applied twice to the already updated local checkout.
GUI mutation results concern the recorded snapshot, not an independently
reproducible GUI build from this PR's base alone.

## How to check tests in practice

Start from the assignment or contract, then derive expected results independently
of the implementation. A financial test should state exact input units, prices,
credit and expected output amounts; its expected result should not call the
production calculator. Review whether assertions cover errors, response content,
persisted effects and absence of partial financial writes. Verify a rejection and
the adjacent valid boundary together.

Mutation testing changes the implementation rather than the input. A changed
comparison, omitted validation, discarded accumulated charge, wrong status or
missing checkpoint should make a relevant test fail. Standard reporting separates
killed, surviving, uncovered, timed-out and invalid mutants; an equivalent edit
can survive without demonstrating a missing requirement.
See [Stryker's result definitions](https://stryker-mutator.io/docs/mutation-testing-elements/mutant-states-and-metrics/)
and [equivalent-mutant guidance](https://stryker-mutator.io/docs/mutation-testing-elements/equivalent-mutants/).

Recommended workflow for this repository:

1. Run an unmodified baseline with `-count=1`. Record the commit, test selection
   and database/time-zone setup. A failing baseline makes the experiment inconclusive.
2. Work in an isolated checkout and owned database. Apply exactly one production
   mutation; keep tests unchanged for the first pass. Make sure the selected test
   really runs and the changed path is reached.
3. Inspect the failure. A compile error, unavailable database, or setup failure
   unrelated to the intended defect does not establish a useful assertion.
   Use bounded timeouts, but prefer a specific failed assertion.
4. For a survivor, inspect the actual input and assertion. Check the outer suite,
   reachability, caller invariants and equivalent behavior before adding a test.
5. Add the smallest readable scenario that distinguishes correct and faulty
   behavior. Include the neighboring valid case, error details and financial
   state where affected. The scenario must pass on the original code.
6. Repeat the same mutation and require the new scenario to fail. Restore the
   implementation, verify the production diff is empty, then run the required
   clean full suite and applicable race/documentation checks.
7. Commit regression tests and reviewed evidence. Keep temporary mutations,
   audit databases and local orchestration out of the application change.

For routine PRs, use fast unit tests, static checks and real database acceptance
where the change affects persistence. Run focused mutation audits when changing
financial boundaries or reviewing AI-generated tests; a broader audit can be a
separate task. This is a recommendation, not a newly enabled CI job or schedule.
Review useful survivors instead of pursuing a percentage with redundant tests.

Fuzzing complements mutations by generating varied inputs. Go's native fuzzing
is useful for parsers and precise-number conversions; deterministic properties
can express invariants such as round-trip preservation and balanced invoice
lines. See the [official Go fuzzing guide](https://go.dev/doc/security/fuzz/).
Race detection checks executed concurrency paths and cannot cover schedules that
never run; see the [Go race detector](https://go.dev/doc/articles/race_detector).
Real PostgreSQL tests remain necessary for transactions, uniqueness, locking and
rollback. Browser tests remain necessary for wiring forms to their handlers.

## Equivalent survivors

| Mutation | Why no new assertion is required |
| --- | --- |
| M011 | At equal credit and charge, selecting either immutable equal amount gives the same allocation. |
| M018, M020 | Including zero in the sign guard cannot satisfy a comparison beyond an int64 endpoint. |
| M031 | Valid JSON has closing syntax after a surrogate pair; the pair cannot end at the complete document length. |
| M044 | Assignment counts are multiples of six and cannot equal 10_000. Custom plans can equal 10_000 and now have a separate test. |
| M070 | Attempts start at one; the extra zero-limit branch still cannot match attempt zero. |

## Validation

The following checks passed after restoring every production edit:

- `make fmt`, `make check` and `git diff --check`.
- The full `make test` suite in a fresh isolated Compose project, including all
  SQL files in UTC and Asia/Shanghai, all Go packages, real database/HTTP
  acceptance and executable worker/simulator workflows.
- `make docs-check`, including the strict build and publication scenarios.
- Unit race checks for accounting, billing, HTTP, simulator, usage and worker;
  integration race checks for ProcessBatch, PostgresInsertBatch and GrantCredit.
- All 114 final Go mutations, 48 SQL mutation executions and 22 GUI mutations;
  86 tests on the completed local GUI with the regression patch.

The standard API image lacks a C compiler, so its initial race attempt could not
build. The successful race runs used the existing compiler-equipped verification
image with the same Go 1.27.2 toolchain. No runtime image or dependency changed.

The test changes preserve existing assertions, isolated schemas, rollback, retries, cleanup and
concurrency scenarios. Standard project commands remain the operational entry
point; no custom Go checker or persistent mutation service was added.
