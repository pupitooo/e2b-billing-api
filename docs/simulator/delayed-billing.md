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
