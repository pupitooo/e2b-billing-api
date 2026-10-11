# Delayed billing scenarios

Use this guide for platform backlogs that arrive after an invoice and for related
manual billing recovery. The [scenario catalog](billing-scenarios.md) remains the
short index; [scenario format](scenario-style.md) describes literal expectations.
Keep each complex timeline here rather than expanding the catalog into a long
sequence of financial examples.

## Platform backlog across September–January

[billing-delayed-platform.json](billing-delayed-platform.json) is shared by the
GUI, CLI and integration tests. It models an Acme platform outage: October's
USD 200 consumption physically enters `usage_inbox` only in November after the
October invoice. December's USD 2 is delayed November consumption. These origin
months are explicit scenario assumptions; the outage description alone does not
identify every measurement's origin.

Acme receives a scenario-only September price of 5 cents per million through
`POST /prices`; the assignment seeds remain unchanged. September has nine varied
increments across sandbox identities, including one unit and zero units, totaling
2_000_000_000 units and USD 100. October and subsequent consumption uses Acme's
seed override of 4 cents per million. A constant USD 50 gross usage limit includes
all measured consumption even after the platform exceeds it.

The add-on is bought on 1 September and cancelled on 20 September. Its entire
September fee remains payable. A later purchase on 30 November owes the whole
November fee and recurs thereafter. The implementation uses an inclusive final
charged cancellation month and forbids overlapping activation months; the
assignment requires full active-month fees but does not define cancellation.
Alternative immediate cancellation would still owe the full cancellation month,
while proration would conflict with the assignment's full-price rule.

The USD 100 grant is processed before October's delayed USD 5. Credit pays newly
processed usage, including historical usage, without rewriting earlier debits.
Processing those receipts before the grant would produce different net invoices;
the additional integration cases demonstrate that allocation-order policy.

| Billing month | Usage on invoice | Add-on | Used credit | Total | Issued at UTC |
| --- | --- | --- | --- | --- | --- |
| September | USD 100 September | USD 20 | USD 0 | **USD 120** | 1 October |
| October | USD 5 late September | USD 0 | USD 5 | **USD 0** | 1 November |
| November | USD 200 late October | USD 20 | USD 95 | **USD 125** | 1 December |
| December | USD 2 late November + USD 120 December | USD 20 | USD 0 | **USD 142** | 1 January |
| January | USD 0 | USD 20 | USD 0 | **USD 20** | 1 February |

Combined exact usage is USD 427; USD 100 credit and four USD 20 monthly add-on fees
produce USD 407 of invoice totals. The January issuance checks December's invoice;
the scenario also invoices January's own calendar period in February.

Limit status uses the **consumption month**, never the invoice total: September
ends at USD 105 and October at USD 200; November has USD 2, December USD 120 and
January USD 0. Delayed October usage does not make November's current-month limit
reached. Every usage invoice retains original month, units, exact gross and credit;
all five complete snapshots are reloaded after the backlog and must remain equal.
Lost replies and January retries preserve the original receipt times and allocate
no duplicate credit, charges, subscriptions or invoice numbers.

## Run from the GUI or CLI

Start with fresh assignment accounts in a separate environment. Apply migrations,
including `013_addon_cancellations.sql`, before using the updated API. In the GUI
open **Assignment demo → Delayed platform billing**. Each step shows its required
`business_time`, request and literal expected response. The GUI refreshes portal
startup metadata before sending and preserves the cursor when the clock differs.

Use the same fixed clock for API, worker and portal. For each phase set the clock
through startup configuration, preserving the existing database volume:

```sh
E2B_DEMO_NOW=2026-09-01T00:00:00Z make up
make migrate
```

For the next phase, replace that value and run `make up` again; reload the GUI.
Clock phases are 1 September, 20 September, 30 September, 1 October, 1 November,
30 November, 1 December, 31 December, 1 January and 1 February. The fixture carries
the year and exact UTC instant beside every step. Do not reset volumes between
phases or reuse accounts already invoiced by a different fixture.

The CLI reads the same fixture with a durable cursor:

```sh
make simulate SCENARIO=billing-delayed-platform MODE=step \
  SOURCE=delayed-platform STATE=/state/delayed-platform.json
```

Run one step at a time and change the trusted clock before crossing its declared
phase. `business_time` is metadata, never a caller-supplied HTTP clock. CLI fast
mode cannot advance service clocks itself.

## Related regression coverage

`TestBillingSimulatorExecutable/multi-month_platform_delay_with_real_receipt_clocks`
executes all 35 public steps in new CLI processes against one private schema, real
HTTP router and worker. An atomic test clock is shared by ingestion, financial
commands and worker; raw receipt timestamps are checked independently of invoice
results. Final storage must contain 15 unique rated receipts, five invoices and
one cancellation, with USD 407 total and no remaining credit.

`TestCloseMonth/platform_and_worker_backlog_boundaries` checks four distinct cases:
a closed origin routes to the receipt month; another closure before worker
processing moves it forward again; an unissued origin keeps its original month;
and a grant after rating does not retroactively pay previously processed usage.
Each preserves every earlier snapshot and the original price and consumption month.

`TestCancelAddon`, `TestCancelAddonAPI`, and
`TestPurchaseAddon/cancellation_and_later_activation` check ownership, explicit
calendar dates, changed retries, closed-period rejection, the purchase-day and
next-month boundaries, and full fees on last-day reactivation at a changed catalog
price. Rejections compare complete financial state. Deterministic database gates force both cancellation/closing lock orders; concurrent command keys retain one termination. Upgrade tests preserve existing purchases and frozen snapshots, and SQL tests run lifecycle constraints in UTC and Asia/Shanghai. These are representative correctness
workflows; the USD backlog amounts do not establish a throughput capacity.

## Missed manual invoice

[billing-missed-invoice.json](billing-missed-invoice.json) executes 27 public steps
through the same GUI and CLI. October is invoiced normally. The operator forgets
November. In January, the operator requests billing through December. The ordinary
single-month endpoint correctly rejects the skipped predecessor; the new catch-up
command issues separate November and December invoices together.

| Billing month | Gross usage | Used credit | Add-on | Total | Issued at UTC |
| --- | --- | --- | --- | --- | --- |
| October | USD 10 | USD 10 | USD 20 | **USD 20** | 1 November |
| November | USD 8 + USD 2 delivered in January | USD 5 | USD 20 | **USD 25** | 1 January |
| December | USD 12 delivered in January | USD 0 | USD 20 | **USD 32** | 1 January |
| January | USD 1 late December + USD 4 January | USD 0 | USD 20 | **USD 25** | 1 February |

The USD 15 credit grant covers usage only. Combined usage is USD 37; credit of
USD 15 plus four USD 20 add-on fees gives **USD 102**, preserving every charge.
November remains open until catch-up, so its January receipt joins November's
original group rather than receiving a late-usage label. December usage received
and processed after catch-up appears on January's invoice with its original price
and consumption month. Previously issued snapshots remain unchanged.

The GUI offers **Assignment demo → Recover a missed invoice** and an independent
**Invoices → Recover missed monthly invoices** form. Clock phases are
`2026-10-31T00:00:00Z`, `2026-11-01T00:00:00Z`, `2026-11-30T00:00:00Z`,
`2026-12-01T00:00:00Z`, `2027-01-01T00:00:00Z`, `2027-01-31T00:00:00Z`, and
`2027-02-01T00:00:00Z`. Use fresh Acme seed data in a separate environment,
preserve the database and cursor between phases, and apply the startup procedure
above. CLI execution uses `SCENARIO=billing-missed-invoice MODE=step` with its own
`SOURCE` and `STATE`.

```json
{
  "idempotency_key": "recover-november-december",
  "through_month": "2026-12"
}
```

Send that body to `POST /customers/acme/invoices/catch-up`. The result contains
`customer_id`, `through_month` and chronological `invoices`, each independently
readable through the existing invoice GET. Keep the same key and body on retry,
including after a lost response. A successful key permanently returns its original
range even after later months close. Changed targets with the same key conflict.
A new key for an existing target returns only that target's original snapshot.

The implementation begins after the latest closed month. On a first run it begins
at the earliest known raw usage, rated group or purchase month, including pending
and quarantined receipts. It then issues every intervening month, including empty
months, under one account lock and transaction. The trusted issue timestamp is
sampled once; invoice numbers, snapshots, freezes, closures and successful command
history commit together. A later-month failure rolls all publication writes back.
Credit is not allocated again. Pending receipts do not block the batch; subsequent
worker processing routes beyond the complete committed range without losing usage.

At most 12 missing months fit one command to bound transaction length and account
lock duration. A larger gap returns `422` without effects: request an earlier
`through_month` first, then a new key for the remaining range. This is a recommended
operational policy, not an assignment requirement. A durable asynchronous catch-up
job with progress and retries is an alternative for very long histories. Automatic
monthly scheduling and invoice delivery remain separate proposals.

`TestCatchUpInvoices` covers missing predecessors, empty and add-on-only periods,
cancellation, first-run obligation discovery, completed-month and 12/13-month
boundaries (including UTC discovery in UTC and Asia/Shanghai), durable retries,
customer/command scopes, concurrent keys and single-month closing, split recovery
of longer gaps, and complete rollback
when the second invoice fails. Worker-cutoff tests verify one rating and one group
freeze across both processing orders. `TestCatchUpInvoicesAPI` verifies explicit
transport fields and no financial effects on errors.
`TestBillingSimulatorExecutable/missed_manual_invoice_with_real_receipt_clocks`
checks all 27 CLI/API steps, six physical receipt timestamps, four immutable
invoices and USD 102 total. GUI tests execute the actual selection, clock gate,
command construction and multiple-snapshot rendering handlers.
