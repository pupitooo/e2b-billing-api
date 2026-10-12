# Test quality and mutation audit

A passing suite does not establish that it would detect a wrong implementation.
This focused audit deliberately changed one production rule at a time, ran the
relevant tests, restored the rule, and added regression scenarios for meaningful
survivors. Production behavior and migrations remain unchanged.

## Financial rule sensitivity

The focused [business-rule evidence](business-rule-mutations.json) records five
compiling, behavior-changing mutations. For each edit, the selected unmodified
test passed and the mutated implementation failed a concrete assertion. The
record contains exact edits, commands, source hashes and output. Compiler errors,
setup failures and timeouts do not count as detection. Production files were
restored after each experiment; deliberate faults are absent from the deliverable.

| Mutation | Deliberate fault | Concrete detecting scenario |
| --- | --- | --- |
| B001 | Remove the older active add-on check from first closing | `TestCloseMonth/add-on_months_cannot_be_skipped/older_add-on_alone_blocks_November/reject_skipped_October` |
| B002 | Change exact limit reach from `>=` to `>` | `TestLimitReached/exactly_the_limit_is_reached` |
| B003 | Prefer a default to an eligible customer override | `TestRate/Acme_October_usage_selects_its_customer_override` |
| B004 | Round each usage_inbox row and sum its booked cents instead of rounding the group | `TestProcessBatch/partitioned_usage_preserves_one_financial_group/half-cent_fragments_round_as_one_group/two_exact_half-cent_fragments` |
| B005 | Credit an identical grant replay again | `TestCreditAPI/rejection_preserves_complete_financial_state/original_retry_still_grants_once` |

All five selected mutations were killed by assertions. This sample establishes
sensitivity for those specific faults; it is not a repository-wide mutation score
and does not prove absence of all defects. Two exact half-cent fragments make the
rounding distinction explicit: their group totals one cent while usage_inbox row rounding
would incorrectly book two. Neighboring fragments and upfront partial credit
retain their separate literal scenarios.

The add-on regression also fails against the original reviewed commit
`4691636` (resolved full identity is in the evidence) and passes with the adopted
fix. That check uses the original production checkout and only adds the regression
scenario. The failed assertion observes HTTP `200` where the skipped-month
operation requires `409`; baseline success after the fix is recorded separately.

The grant's initial trial selected only the nested replay step and skipped the
preceding grant. That trial passed because it exercised a first grant, not the
history branch. Selecting the complete owning workflow exercises the retry and
kills the mutation. The evidence retains this uncovered-filter diagnosis; it is
not treated as an equivalent mutation or as detection. Stateful workflow filters
must include their preparation steps.

## Broader experiment evidence

The [broader mutation record](test-mutation-audit.json) preserves 114 Go, 24 SQL
and 22 GUI experiments, including exact edits, results and equivalent-mutant
rationale. SQL experiments ran in both UTC and Asia/Shanghai. Their raw input
names and hashes are audit data; implemented requirements and executable filters
are described by [test suites](testing.md#business-rule-traceability).

Some recorded targets are retired: the batch envelope accepts only `events`, and
financial operation identity is held in `api_idempotency_operations`. Retired
metadata and ledger-identity experiments are not counted as active coverage of
those interfaces. The focused experiments above exercise the shared command
history, microcent names and add-on closing rule used by the application.

The broader record's six equivalent Go survivors require no redundant assertion;
the explanations below describe why their behavior does not change. The focused
five-rule sample contains no survivor. Reevaluate a future survivor through
reachability, input selection, actual assertions and equivalent behavior before
adding tests to pursue a percentage.

## What needed improvement

| Responsibility | Demonstrated gap | Added verification |
| --- | --- | --- |
| Immutable money | Replacing `Microcents()`'s defensive copy with a pointer survived. Setting the returned integer to zero replaces its slice header and can miss shared nonzero storage. | Overwrite it with one and check both the original amount and a value copy. |
| Calendar routing | Routing late November usage into December of year 9999 had no adjacent valid test. | Literal December result in the domain and a real processor scenario checking every financial projection; existing exhaustion/quarantine cases remain. |
| Financial validation | Direct tests covered identifier size but omitted malformed Unicode, blank/NUL identifiers, timestamp precision and zero-versus-positive cents. | Exact validation fields/messages, valid offset/year neighbors, zero price/limit and rejected zero grant. |
| Invoice addition | Rejecting the valid maximum sum could pass, as could rejecting the valid negative minimum in the signed helper. | Both inclusive limits and their immediately overflowing neighbors. |
| Usage HTTP | Unicode surrogate endpoints needed explicit coverage; M024's envelope metadata size rule has been removed. | Event identifiers retain ASCII/Unicode 256-byte boundary coverage, valid endpoint pairs, and rejected unpaired endpoint escapes. Removed envelope metadata is rejected before storage. |
| Simulator transport | Some configuration endpoints, HTTP 500, non-JSON acknowledgements, a single duplicate copy and exact body/file/response limits were missing. | Named direct scenarios, literal byte counts and transmission counts; the exact one-MiB batch uses valid identifiers. |
| Worker validation | Missing guards could hang invalid-configuration cases until the package timeout. | Local cancellation budgets preserve processor/heartbeat checks and yield immediate test failures for the same mutations. |
| SQL integrity | Removing positive schema-version, positive invoice-sequence, nonnegative invoice-total or object-snapshot checks survived all SQL files. | Named SQL with expected `23514`, plus adjacent valid values and rolled-back fixtures. |
| GUI | The last safe integer, zero grant, equal usage endpoints, schema version, extra invoice lines, complete captured snapshots, journal preservation, path restrictions and Retry-After were not all asserted. | Extend existing operation tests and add direct expectation/snapshot scenarios; the frontend suite includes these regression cases. |

Go accounting unit coverage was already 97.4% before this audit, yet the mutable
money-storage mutation survived. Reaching a statement and asserting its contract
are separate properties. No production bug is claimed here: the deliberate
faults demonstrate weaknesses in test inputs or assertions.

## GUI verification

The GUI regression cases are included in `tests/demo/` and run through
`make demo-check`. The [GUI regression patch](gui-test-regressions.patch) is an
audit artifact and must not be reapplied over those cases. Calendar-date commands,
credit without a caller timestamp, complete invoice lines and retained retry
identities are part of the current executable checks.

Module and transport assertions do not establish full browser rendering or every
form-to-handler interaction. The mutation sample does not cover all `app.mjs`
behavior; browser interaction remains a separate verification responsibility.

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

The financial-rule audit requires a passing baseline, five assertion failures for
valid mutations, and a clean restored production diff. Its associated regression
checks include repository formatting/static checks, the full PostgreSQL inbox
suite, the complete `make test` gate with SQL integrity in both time zones, and
strict documentation/publication checks.

Selected recovery and purchase/worker closing workflows passed ten repetitions
under the race detector with the pinned Go 1.27.2 toolchain and a C compiler.
Race detection covers executed schedules; it does not prove every interleaving.
The regression changes retain existing scenarios, cleanup, rollback and private
schema isolation. No custom Go checker or persistent mutation service is added.
