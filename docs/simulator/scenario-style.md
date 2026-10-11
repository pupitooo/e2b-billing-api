# Readable platform scenarios

A scenario states one complete outcome and owns an isolated source namespace and state file.
Its named steps declare inputs and literal expected outputs before execution. Sequential steps
may share state when they verify that single workflow; distinct workflows get independent cases.

Transport scenarios expose command arguments, expected process error, stored event count,
expected CLI output, and whether the whole inbox must remain unchanged. Measurements explicitly
name customer, metric, interval endpoints, and units. Named assignment phases keep their hourly
measurements together, with the late-October operator barrier visible beside its input.

For public billing scenarios, use the same structure: a step name, HTTP method/path, request body,
expected status, and literal response fields. Put retry/fault behavior on the step it affects.
Use a named wait step to observe asynchronous processing before checking balances or limits.
An issued-invoice check must show every line amount, the total, and exact used credit.

Document each executable or manual scenario with one numbered table using
**Step**, **Simulator action**, and **Expected result** columns. For a manual
workflow, the action column also identifies the operator's requests. Put concrete
inputs, faults, and retry identities beside the expected HTTP status, amount,
state, or process outcome. Preserve one row per executable JSON step; separate
distinct manual requests into their own rows. Show exact amounts when rounding
matters, and distinguish durable acceptance from completed processing.
Keep the scenario's behavior description exclusively in its step table. Remove
parallel prose summaries; link other guides to the canonical table instead of
maintaining another description. Keep setup, commands, prerequisites, and general
runtime contracts outside the table. Tables summarize the literal JSON/test
expectations; they do not replace executable assertions.

Helpers may establish isolated resources, supply irrelevant transport headers, and read results.
They must not calculate expected amounts with production pricing or rounding code. Check process,
HTTP, or database errors before comparing outputs. Report the operation, inputs, actual result,
and expected result when a step fails. Retain durable state on failures so a new producer process
can retry the same identities and content.

The transport executable tests demonstrate this style in `tests/api/simulator_test.go`.
Public billing workflows are documented in [billing scenarios](billing-scenarios.md).
Saved assignment event identities,
step names, ordering, barriers, and the custom JSON format remain compatible.
