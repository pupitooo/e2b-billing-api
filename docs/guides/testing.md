# Test suites and scenario filters

`make test` is the primary test command. Start PostgreSQL with `make up SERVICE=postgres` before running all tests or the database suite; the Go suite builds and starts the API and worker automatically.

Follow the [final acceptance test procedure](final-acceptance-testing.md) to verify every assignment requirement, walk through the October/November example with explicit checkpoints, inspect persisted accounting state, and exercise outages, retries, and restart in isolated Compose projects.

| Command | Purpose |
| --- | --- |
| `make test` | Run SQL, Go integration and isolated backend E2E suites. |
| `make e2e-test` | Run all backend E2E scenarios against separate API/worker/PostgreSQL containers. |
| `make test SUITE=e2e` | Run only the isolated backend E2E suite. |
| `make go-test` | Run Go package tests without starting external services. |
| `make db-test` | Run only the database integrity suite. |
| `make api-test` | Start PostgreSQL, migrate, and run HTTP/database acceptance tests against the API. |
| `make inbox-test` | Start PostgreSQL and run repository tests in private schemas. |
| `make adminer-check` | Check integer display syntax, exact bigint grouping, type boundaries, and links without starting services. |
| `make test SUITE=go` | Run Go unit, PostgreSQL repository, HTTP acceptance, and worker lifecycle tests; services start automatically. |
| `make test SUITE=db` | Run only the database integrity suite in both configured time zones. |
| `make test RUN='^TestPostUsageBatches$/^usage_batches_happy_path$'` | Run only the named Go scenario. Supplying `RUN` selects the Go suite by default. |

`SUITE` accepts `all` (the default), `go`, `db`, or `e2e`. `RUN` uses the standard [Go `-run` regular-expression filter](https://go.dev/src/cmd/go/internal/test/test.go): anchors select an exact test name; a pattern such as `Usage` selects matching names. Explicit `SUITE=all RUN=Usage` runs the full SQL suite and matching Go tests. Unknown suites and `SUITE=db` combined with `RUN` fail before starting test work.

The [handler tests](../../internal/httpapi/handler_test.go) use `httptest` to check routes, method restrictions, and the current usage response without a running server. With a local Go toolchain, run `go test ./...`; the integration build tag keeps external API tests out of this command. `make go-test` runs the same package tests in a container without starting services.

The SQL suite applies pending migrations and discovers all `tests/sql/*.sql` files. [Inbox tests](../../tests/sql/usage_inbox.sql), [billing model tests](../../tests/sql/billing_model.sql), and [assignment seed tests](../../tests/sql/assignment_seed.sql) run in UTC and `Asia/Shanghai` and roll back their data. Seed checks use a private schema, preserving edited application data. Output identifies the file and time zone; any failure makes the command fail.

When a demo clock is configured, use `E2B_DEMO_NOW= make api-test` for the
ordinary receipt suite, which checks timestamps against real time. The E2E
launcher supplies its own clock phases and ignores the demo environment.

The original [API happy-path request](../../tests/api/usage_batches_test.go) and response
expectations remain unchanged. The [acceptance tests](../../tests/api/usage_batches_test.go)
send HTTP requests to the running service and inspect committed rows through
a separate PostgreSQL connection. They cover durable receipt, preserved retry
metadata, atomic conflicts, invalid later events, and concurrent HTTP retries.
They remove only their owned rows, retaining any pre-existing fixed happy-path
fixture. The [repository tests](../../tests/inbox/usage_inbox_test.go) additionally
exercise deterministic lock waits, competing commits/rollbacks, canceled
transactions, and each conflicting content field. Each test drops its private
schema. The API stays running for exploration; `-count=1` executes every test.

Worker package tests cover loop cancellation, deadlines, retry pacing, heartbeat
health, and bounded shutdown. The [worker process tests](../../tests/worker/lifecycle_test.go)
start the compiled executable against PostgreSQL without an API dependency, verify its
health probe, send SIGTERM, and require a clean exit with heartbeat cleanup. They
also reject invalid startup configuration. Run them through `make test`, or
filter with `make test RUN='^TestWorkerExecutable$/^worker_process_lifecycle$'`.

Simulator unit tests verify exact fixture totals, splitting, barriers, durable
recovery, file locking after a killed process, safe retries, and retained errors.
The [simulator HTTP acceptance tests](../../tests/api/simulator_test.go) launch the
separate executable against the running API, inspect committed PostgreSQL rows,
and remove only their owned producer namespaces. Run them with
`make test RUN=Simulator`; they also run automatically in `make test` and CI.
The [public billing workflow tests](../../tests/inbox/billing_simulator_test.go) use the
same CLI with a real HTTP router, accounting worker, and private seeded schema per
scenario. They verify complete literal invoices and balances, monthly limit reads,
HTTP `503`/`Retry-After`, lost committed replies, and restart with retained state.

`TestInvoiceAmounts` covers uncredited USD 0.001, one microcent below half a cent,
and the exact half-cent boundary. `TestAssembleInvoice` retains zero-cent usage
lines with complete units and exact microcents even when different price groups'
combined gross exceeds half a cent. The public
[three-month scenario](../simulator/billing-scenarios.md#zero-total-invoices-without-remainder-carry)
verifies three positive USD 0.002 charges produce three zero-total invoices,
ordinary sequential numbers, immutable reads/retries, and no remainder carry
or credit debit. Run it in a private seeded schema with:

```sh
make inbox-test RUN='^TestBillingSimulatorExecutable$/^three_zero-cent_invoices_retain_usage_without_carrying_rounding_remainders$'
```

`make demo-check` also verifies that the invoice view retains measured units,
the exact positive charge, and the zero payable amount on issue and load.

With Go 1.27 or later installed locally, the same test can target a running API directly:

```sh
E2B_API_URL=http://127.0.0.1:8081 \
E2B_TEST_DATABASE_URL='postgres://e2b:e2b_local_dev@127.0.0.1:5432/e2b_billing?sslmode=disable' \
go test -tags=integration -count=1 -v ./tests/api ./tests/inbox
```

Read [backend E2E scenarios](e2e-testing.md) for the real-container coverage,
clock transitions, filters and CI evidence. The [manual walkthrough](e2e-walkthrough.md)
lets you inspect each intermediate output independently. GUI tests are Node tests
with a simulated DOM and mock HTTP responses; GUI is outside backend E2E.

[CI](../../.github/workflows/ci.yml) runs `make check` and the complete test gate on every push and pull request, using a fresh PostgreSQL volume and the same Compose configuration. Separate CI steps identify SQL integrity, unit tests, PostgreSQL workflows, HTTP tests against the running API and worker executable checks. The separate `Backend E2E` job executes `make e2e-test`, completing the same suites as unfiltered `make test`; both jobs are mandatory. The required `All tests` check includes Go formatting and static analysis, SQL integrity tests, Go package tests, PostgreSQL repository tests, HTTP integration tests, and worker lifecycle tests; the branch must also be up to date with `main`. Failed runs include service logs, and each run removes its test containers and volume.

Read the [test-quality and mutation audit](test-mutation-audit.md) for the findings, regression scenarios, exact mutation evidence and a practical workflow for reviewing AI-generated tests.

## Calendar commands and credit audit time

`TestCommandDate` and `TestValidateDate` cover supported years, leap days,
strict date-only input, and UTC midnight. `TestUTCDay` covers offset conversion.
`TestCreatePrice` checks today at noon, past-date rejection, rated-history
protection, stable retries, and catalog waiting within a day or across midnight.
`TestPriceAPI` checks request and response dates and midnight persistence.
`TestPurchaseAddon` covers past open, today, future, and closed-month boundaries;
`TestAddonAPI` rejects timestamps and invalid dates. `TestGrantCredit` checks
server-owned audit time, UTC/microsecond precision, supported clock boundaries,
and replay without ledger, balance, version, or command-history changes.
The GUI's `calendarDate` and `buildCommand` tests verify the same wire format;
credit commands contain no timestamp. The executable billing simulator checks
unchanged literal assignment totals using the updated public commands.

## Rejection and recovery assertions

The financial API tests compare complete catalog, receipt, rating, group, monthly
usage, ledger, subscription, command, account and invoice rows before and after
rejected commands. The scenarios pair zero grants with one cent, negative prices
with free prices, invalid calendar days with valid month-end purchases, and
changed-content retries with the original operation. Valid recovery retains the
same operation key and creates only the declared financial effect.

`TestCloseMonth/rejection_preserves_complete_financial_state` rejects a skipped
month with nonempty credit, ledger, rated usage and subscription rows. It then
compares every field of the adjacent valid invoice, including price identity,
units, original month, exact gross/credit, all lines, number and buyer snapshot.
`TestPostgresInsertBatch/conflict_preserves_rated_financial_state` checks complete
rollback of a batch containing both new input and a conflicting rated receipt.

`TestSpendLimitAPI` verifies the persisted value and command count after changes,
explicit `null` for unlimited configuration, unchanged state after invalid input,
and concrete response fields on both sides of the UTC month boundary. The clock
has an explicit offset to distinguish the local calendar from the UTC month.

```sh
make inbox-test RUN='^(TestSpendLimitAPI|TestPriceAPI|TestCreditAPI|TestAddonAPI|TestCloseMonth|TestPostgresInsertBatch)$'
```

## Generated accounting sequences

`TestProcessBatch/generated_accounting_sequences` executes three stable seeds,
each with 24 generated commands per month plus October/November closes and
closing retries. Inputs combine grants, measured increments, repeated identities,
nullable spend limits and late original-October usage after its publication.
Expected states are prepared before execution by a small independent reference
model using integer microcents and exact rational half-up rounding. It never calls
production rating, allocation, routing or presentation functions.

After every command the test checks balance and ledger conservation, nonnegative
credit bounded by gross, exactly one rating per completed receipt, group units and
debits, original-month gross, limit responses, immutable invoice snapshots and
all financial line fields and sums. Stable seeds and named steps reproduce a
failure without relying on a random CI environment.

`TestProcessBatch/partitioned_usage_preserves_one_financial_group` uses literal
expectations for split and unsplit measurements at rounding and partial-credit
boundaries. Every fragment has the same price and invoice conditions. Its only
grant precedes every fragment; no grant or closure interleaves them. This property
does not promise general independence from arrival order or invoice timing.

```sh
make inbox-test RUN='^TestProcessBatch$/(generated_accounting_sequences|partitioned_usage_preserves_one_financial_group)$'
```

## Commit, response and checkpoint recovery

`TestBillingSimulatorExecutable/committed_usage_survives_response_loss_and_producer_death`
uses the real HTTP router, PostgreSQL and a separate simulator executable. One
case closes the HTTP connection after committing the receipt; another withholds
the reply and kills the producer only after observing that commit. Channels and
the committed database row determine the failure point. Timeouts bound failures;
sleeps do not choose which operation wins.

Both cases retain the original unadvanced checkpoint, reopen storage through a
new pool/store, process the receipt once, and resume a new producer process. The
replayed request body must be identical. Full-row comparisons, literal balance
and ledger counts and accounting invariants prove no additional financial effect
and preserve the original receipt timestamp despite the restarted API clock.

| Failure boundary | Automated scenario | Required outcome |
| --- | --- | --- |
| Before transaction commit | `TestProcessBatch/late_database_failure_rolls_back_all_financial_effects`; command/resource rollback cases | No partial financial write; valid retry succeeds. |
| After commit, lost HTTP reply | `TestBillingSimulatorExecutable/committed_usage_survives_response_loss_and_producer_death/committed_HTTP_response_is_lost` | Original receipt survives; unchanged retry has no second effect. |
| After commit, producer dies before checkpoint | `TestBillingSimulatorExecutable/committed_usage_survives_response_loss_and_producer_death/producer_killed_after_commit_before_checkpoint` | Durable cursor stays at zero; new process completes the same plan. |
| Retained state and fresh producer | Both new cases and the existing lost committed grant workflow | Original identity, input, receipt timestamp, ledger and balance are preserved. |

```sh
make inbox-test RUN='^TestBillingSimulatorExecutable$/committed_usage_survives_response_loss_and_producer_death$'
```

These process and transport tests establish replay across the tested boundaries.
They do not simulate disk loss, PostgreSQL crash recovery, every OS signal point,
or failure of the database host itself.

## Business-rule traceability

Each row connects the assignment or adopted contract to a valid example, its
boundary/conflict, financial assertions and executable operation test. The
[assignment walkthrough](acceptance/assignment.md) additionally verifies the
complete numerical example through the public API and separate executable.

| Requirement / contract | Valid scenario | Boundary or conflict | Financial assertions | Operation / workflow test |
| --- | --- | --- | --- | --- |
| Durable incremental usage | New receipt with explicit values | Changed content after a new batch row; identical retries | Complete rollback, original receipt time, one rating and one financial effect | `TestPostgresInsertBatch`; `TestPostUsageBatches`; `TestProcessBatch` |
| Historical prices and customer overrides | Original October default or eligible customer price | Price activation instant, conflicting retry, negative price beside zero | Exact price identity, units, original month, group and monthly gross | `TestRate`; `TestCreatePrice`; `TestPriceAPI`; `TestProcessBatch` |
| Credit grants and allocation to usage | Distinct positive grants and partially covered usage | Zero beside one cent, exhausted balance, changed/repeated identity | Ledger equals balance, credit bounds, independent group debits, no second grant | `TestGrantCredit`; `TestCreditAPI`; generated processor sequences |
| Full recurring add-on fee | First/last purchase day; consecutive October and November invoices | Older add-on without usage; future add-on; both purchase/close lock orders | Exactly one full fee per month, no skipped obligation, unchanged subscription and failed-close state | `TestPurchaseAddon`; `TestAddonAPI`; `TestCloseMonth/add-on_months_cannot_be_skipped`; purchase/closing lock workflow |
| Gross monthly spend limits | Finite and explicit unlimited configuration | Below/equal threshold, zero/negative limit, UTC month boundary | Stored limit and version, concrete gross/month/reached fields, unchanged failed configuration | `TestLimitReached`; `TestSetSpendLimit`; `TestSpendLimitAPI` |
| Monthly invoice presentation | Assignment and subcent totals | Group half-cent boundary, upfront partial credit, split increments | Every financial line, unit, price, original month, exact gross/credit and cent sum | `TestInvoiceAmounts`; `TestAssembleInvoice`; `TestCloseMonth`; partitioned processor scenarios |
| Immutable issued invoices and late usage | October invoice followed by November and late October usage | Retry, pending cutoff and worker/closing lock order | Complete unchanged snapshot/number; late amount routes forward once | `TestCloseMonth`; generated processor sequences; `TestBillingSimulatorExecutable` |
| Recovery and retries | New producer resumes retained state | Before commit, lost committed reply, process death before checkpoint | No partial write, original key/body/receipt time, one rating, ledger and balance | Command/resource rollback workflows; `TestProcessBatch`; committed usage recovery workflow |

The five focused [business-rule mutations](test-mutation-audit.md#financial-rule-sensitivity)
prove that concrete tests reject deliberately wrong implementations. Statement
coverage alone, status-only checks and compiler failures are not that evidence.
