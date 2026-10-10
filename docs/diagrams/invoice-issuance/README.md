# Implemented monthly invoice issuance

This is the detailed explanation of the [simplified invoice generation workflow](../closing-workflow/closing-workflow.png). It describes the implementation checked on **10 October 2026**. The usage-to-invoice guide embeds the simplified view and links to this detailed PNG in its caption.

![Implemented monthly invoice issuance](invoice-issuance.png)

[Open full-size PNG](invoice-issuance.png) · [Editable Mermaid source](invoice-issuance.mmd).

## Reading the activities

Each bold numbered title identifies one logical activity. Supporting lines describe its inputs, rules, tables or outputs. Diamonds are decisions, solid arrows show normal flow, and dotted arrows show representative failure paths. The numbers identify neither separate transactions nor necessarily separate SQL statements.

The simplified view labels three logical steps, all inside **one transaction**:
A locks and validates, B builds from already processed groups, and C publishes.
The detailed view expands those activities: 2–6 establish the lock and eligibility,
7–18 build financial presentation, and 19–24 publish together. Activities 11–15
repeat for each group. An empty month still supports add-ons and a zero credit line.

The entry is `POST /customers/{customer_id}/invoices` with a `YYYY-MM` month.
Closing is explicitly invoked; document delivery and scheduling are separate.

## Processed usage cutoff and retries

[`CloseMonth`](../../../internal/billing/invoices.go) calls `issueInvoice`, which
locks `customer_billing_state` and reads an existing invoice before performing a
new close. [`validateClosing`](../../../internal/billing/closing.go) requires an
ended UTC month and compatible history: the first invoice cannot skip earlier
existing usage, each later new invoice needs its immediately preceding month
closed, and no target/later closed month or earlier unclosed rated group is allowed.
January becomes eligible exactly at
`2027-02-01T00:00:00Z`; the preceding microsecond is rejected without writes.

Only accounting committed before closing acquired the account lock is included.
`rated_usage_groups` is the financial source. An indexed first-closing existence
check reads the older usage boundary; it does not select usage to process.
Closing never invokes `accountReceipt`, drains a backlog or discovers a missing price.
Pending and quarantined usage in the target month cannot block a correctly ordered invoice.
An in-flight worker already holding
the account lock finishes first; a worker that acquires it after a committed close
reads `closed_billing_months` and routes usage to an eligible open billing month.
Historical pricing and original-month gross spend remain tied to consumption.

There is no fixed pending-usage list or durable intermediate closing attempt.
The lock remains held from eligibility through publication. If publication fails,
all closing writes roll back; retry can include newly processed groups. Once an
invoice commits, retries return that exact snapshot. Migration 009 removes the
former attempt, cohort and exclusion tables while retaining all accounting data.

This policy explicitly accepts missing pending usage on the current invoice to
simplify closing. Different billing months have different group rounding
boundaries, as explained in the [closing contract](../../architecture/accounting-rules.md#transaction-and-closing-contract).

## Financial presentation and atomic publication

[`buildInvoice`](../../../internal/billing/invoice_data.go) reads buyer details, the next customer sequence, exact rated groups and applicable add-on subscriptions while the account lock is held. Usage groups are selected by the **billing month** and ordered by original usage month, metric and price version. Add-ons use the stored subscription price for every month at or after `start_month`, including the purchase month; the current model has no cancellation end month or proration.

[`invoiceUsageLine`](../../../internal/billing/invoice_lines.go) calls [`InvoiceAmounts`](../../../internal/accounting/money.go) for each group:

1. Subtract the already allocated exact credit ticks from exact gross ticks.
2. Round gross and net separately to half-up cents.
3. Derive displayed credit as rounded gross minus rounded net.
4. Produce a gross usage line, retaining units, the historical price, original usage month and exact gross/credit ticks for audit.

The aggregate credit line is the negative sum of these derived deductions. Issuance consumes no additional credit and creates no new credit ledger debit. Add-ons follow usage lines, then the credit line is appended even when zero. [`invoiceTotal`](../../../internal/billing/invoice_lines.go) sums all lines with arbitrary precision and checks the final nonnegative signed 64-bit cent result; a positive intermediate subtotal alone is not grounds for rejection. Individual group rounding and the aggregate credit deduction also have range checks.

The number is built from the uppercase customer ID and the current sequence, padded to at least four digits, for example `ACME-0001`. The account lock reserves that value during construction; the counter advances only with successful publication. An exhausted sequence is rejected before any publication writes.

[`freezeInvoice`](../../../internal/billing/invoice_snapshot.go) commits these effects together:

| Table | Change |
| --- | --- |
| `closed_billing_months` | Record the closed customer/month and issue timestamp. |
| `invoices` | Store the unique numbered invoice and complete immutable JSON snapshot. Lines live in `snapshot.lines`, rather than a separate line table. |
| `invoiced_usage_groups` | Link and freeze all groups included for this customer/billing month. |
| `customer_billing_state` | Increment `next_invoice_number` and `state_version`; leave credit allocation to receipt accounting. |

After commit the API returns `200` with the stored document. Concurrent closers serialize on the account lock, and retries return its original number, buyer details, lines, amounts and issue time. `GET /customers/{customer_id}/invoices/{month}` reads the same snapshot through a separate read operation, outside this chart.

## Failure boundaries

The red failure nodes describe the HTTP mapping in [`writeFinancialError`](../../../internal/httpapi/financial.go). Invalid application inputs and unfinished months return `422`; a missing account returns `404`; financial-history conflicts return `409`. Other failures, including storage deadlines, presentation overflow and sequence exhaustion, currently map to `503` with `Retry-After: 1` when the connection permits a response. A persistent data or calculation problem requires investigation; repeating the request does not repair it.

[`Store.transact`](../../../internal/billing/store.go) enables durable commit and attempts rollback with a cleanup context independent of request cancellation. Failure before commit rolls back that transaction's attempted writes. Previously committed worker accounting remains durable even if publication fails; there are no separately committed capture writes. The operation contains no automatic whole-closing retry loop; the caller retries the same customer/month to read a committed invoice or attempt publication again.

A lost reply or commit error can leave the caller uncertain whether a transaction committed. Read the invoice or repeat closing with the same identity to resolve the outcome. A client disconnect can also prevent delivery of the mapped HTTP response. Dotted arrows illustrate failure handling; a database failure can occur at any database activity, rather than only at the four depicted origins.

## Relationship to the simplified drawing

The simplified and detailed diagrams reflect the processed-usage-only contract
effective on 10 October 2026. The detailed view retains gross/net rounding, JSON
snapshot fields, numbering, atomic writes, validation and error paths. Regression
coverage includes zero/partial/full worker progress, both worker/closing lock
orders, immutable retries, quarantine recovery, UTC boundaries, migration and
publication rollback.

The committed Mermaid source is editable and its PNG preview is readable without generation tooling. The renderer stays local and ignored; no SVG artifact is generated.
