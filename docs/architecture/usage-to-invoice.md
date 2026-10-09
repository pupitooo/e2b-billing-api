# From usage to invoice: ticks, rating, credit, and the database model

Billing answers several different questions in sequence: what the customer consumed, how much that consumption costs, how much credit covers, and what appears on a particular invoice. `RatedUsageGroup` connects measured usage to its accounting result. It retains the shared pricing meaning of many measurements and their exact financial value, so small charges survive rounding.

This document connects the explanation from the "Calculating in ticks" discussion to the project's database model. It follows one small example through the entire flow, then explains price changes, credit allocation, rounding, late usage, and the assignment's invoices.

![Usage events to invoice](../diagrams/usage-to-invoice/usage-to-invoice.png)

[Editable Mermaid source](../diagrams/usage-to-invoice/usage-to-invoice.mmd).

The schema below builds on [inbox migration 001](../../migrations/001_usage_inbox.sql) and [accounting migration 002](../../migrations/002_billing_model.sql), introduced in [PR #8](https://github.com/pupitooo/e2b-billing-api/pull/8). [Migration 003](../../migrations/003_assignment_seed.sql) contains the initial catalog. Migration 004 converts credit to exact ticks. Shared Go calculations for rating, credit, rounding, and UTC months are implemented in `internal/accounting`; the worker does not yet persist their results, and invoice tables remain proposed. The [financial rules](accounting-rules.md) describe the current contract.

The diagram follows the required `Credits → Rounding` order. On 9 October 2026 the user confirmed credit allocation in ticks before rounding. [Migration 004](../../migrations/004_exact_credit.sql) therefore converts balances, allocations, and the ledger to exact ticks. The earlier cent-based design is retained below as a historical alternative; the current policy uses exact ticks.

## 1. Why storing everything in cents is insufficient

Consider the assignment's price: **USD 0.05 per 1_000_000 units** of `cpu_seconds`. In this example one CPU second is one resource unit. The price per million is therefore 5 cents.

The platform sends 1_000 separate events, each containing 1_000 units:

```text
1_000 events × 1_000 units = 1_000_000 units
```

One measurement costs:

```text
1_000_000 units -> 5 cents
    1_000 units -> 0.005 cent -> USD 0.000_05
```

Rounding each measurement immediately to whole cents produces zero. Multiplying zero by 1_000 still produces zero, although a million units should cost 5 cents. The result would depend on whether the platform sent one measurement or 1_000 smaller ones.

We therefore use a smaller exact unit of money:

```text
1 cent = 1_000_000 ticks
1 USD  = 100_000_000 ticks
1 tick = USD 0.000_000_01
```

A small event retains its value:

```text
1_000 units -> 0.005 cent -> 5_000 ticks
```

The group adds the exact values:

```text
event #1          5_000 ticks
event #2          5_000 ticks
...
event #1_000      5_000 ticks
----------------------------
total         5_000_000 ticks
              = 5 cents
              = USD 0.05
```

This resembles adding small lengths in millimeters and displaying the result in meters. Rounding every small segment to whole meters first would lose part of the actual length.

**Keep each measurement's exact value and round the cumulative total of its group.** Ticks prevent premature loss of precision; the rounding rule is a separate decision.

## 2. One example through the entire flow

Cyberdyne consumes a million units before 15 October. The platform delivers them as the 1_000 small events above. In this first walkthrough, the customer has no credit or add-on.

| Stage | Meaning in this example | Where the value is stored |
| --- | --- | --- |
| Usage events | 1_000 measurements of 1_000 units, each with an identity, sandbox, and interval. | 1_000 `usage_inbox` rows, each with `units = 1_000`. |
| Rating | Assign the historical price of 5 cents per million to each measurement; one costs exactly 5_000 ticks. | The price is in `price_versions`; `accounting.Rate` performs the pure Go calculation; a future writer stores links in `usage_ratings`. |
| RatedUsageGroup | One group containing a million units and an exact gross charge of 5 million ticks. | `rated_usage_groups.total_units = 1_000_000`, `exact_charge_ticks = 5_000_000`. |
| Credits | No available credit and no debit. Gross and payable usage are equal. | The balance is in `customer_billing_state`; the group's allocation is zero. |
| Rounding | The whole group produces 5 cents. | A calculation; the current schema contains the cent projection `booked_charge_cents`. |
| InvoiceLine | An immutable "CPU usage for October 2026" line for 5 cents. | The proposed `invoice_lines` table does not exist yet. |
| Invoice | Cyberdyne's October invoice, with lines totaling 5 cents. | The proposed `invoices` table does not exist yet. |

Invoice calculation can work with a few already rated groups instead of loading and pricing every small event again. Individual events remain traceable through their rating links, so aggregation preserves the origin of the charge.

## 3. Usage events: what was consumed

An event might say: "Cyberdyne's sandbox consumed 1_000 `cpu_seconds` during this interval." It is an increment to count exactly once. A repeatedly reported lifetime counter would require a different contract.

In the database this is one `usage_inbox` row:

| Columns | Meaning |
| --- | --- |
| `source`, `event_id` | The measurement's composite primary key; retries and changes in batching use the same identity. |
| `schema_version` | A positive event-contract version, supplied explicitly. |
| `customer_id`, `sandbox_id`, `metric` | Who consumed which resource and in which sandbox. |
| `period_start`, `period_end` | The consumption interval in `timestamptz`; the end must follow the start. Rating uses `[start, end)`. |
| `units` | A non-negative integer `bigint` containing the resource quantity. |
| `received_at` | The server-supplied receipt time. Historical pricing uses consumption time. |
| `processed_at`, `processing_error` | Accounting processing state. Successful completion cannot coexist with an unresolved error. |

The application supports UTC calendar years **1000 through 9999**, including the minimum instant `1000-01-01T00:00:00Z`. Validate these bounds after converting the supplied offset to UTC. For example, `1000-01-01T00:00:00+01:00` falls in UTC year 0999 and is rejected, while `0999-12-31T23:30:00-01:00` falls in UTC year 1000 and is accepted. Calendar years and timestamps use ordinary digits without underscore separators.

The inbox has no financial columns, `group_id`, or foreign keys to customers and metrics. It can durably preserve input whose catalog records are missing; subsequent processing must record that problem visibly.

The unique identity prevents duplicate rows. Ingestion also compares content: an identical retry preserves the original values and receipt time, while different content under the same ID produces a conflict. Successful inbox receipt confirms durable storage. Credit allocation and invoice issuance occur in later stages.

The meaning of a unit belongs to the event contract. Supporting fractions of CPU seconds would require defining another integer measurement unit, for example. Ticks provide a finer unit of **money**; fractional resource units need their own representation.

## 4. Rating: the cost under the historical price

The pure Go calculation `accounting.Rate` performs rating; a future worker will connect it to transactional database writes. This schema has no separate `rating` table that calculates prices. The worker selects the price effective at consumption time and computes the exact gross charge.

`price_versions` stores `price_version_id`, optional `customer_id`, `metric`, `price_per_million_cents`, and `effective_from`. `customer_id IS NULL` denotes a default price. The latest eligible customer-specific price takes precedence over the latest eligible default. Versions are append-only, so price changes preserve historical prices.

For the current price contract:

```text
exact_charge_ticks = units × price_per_million_cents
```

There is no division by a million in this multiplication because one cent already represents a million ticks. At 5 cents per million, the result is `1_000 × 5 = 5_000 ticks`.

The tick column uses the `billing_ticks` domain over exact PostgreSQL `numeric`. It accepts only non-negative finite integers. Aggregated `total_units` also uses integer-valued `numeric`, allowing totals beyond the `bigint` range of one event. Go must use checked integer arithmetic or arbitrary-precision integers. Overflow must fail visibly. These exact financial calculations do not use `float64`.

Both default and customer-specific rates support only whole cents **per million units** in this model. This still allows individual measurements to cost far less than a cent. Finer catalog prices would require a new precision contract; the `bigint` column `price_per_million_cents` cannot store a fractional-cent rate.

### Example: why summing all units and using the current price fails

Use the assignment's actual price changes: 5 and 6 cents per million. The earlier discussion also used an illustrative price of 8 cents, which is outside the seed catalog.

| Cyberdyne consumption | Historical price | Exact result |
| --- | --- | --- |
| 1_000_000 units on 10 October | 5 cents per million | 5_000_000 ticks = 5 cents |
| 1_000_000 units on 20 October | 6 cents per million, effective from 15 October | 6_000_000 ticks = 6 cents |
| Total | Two different price versions | 11_000_000 ticks = 11 cents |

Pricing both million units at the later 6-cent rate would produce 12 cents. Using the earlier 5-cent rate would produce 10 cents. A monthly total of units alone does not identify which rate applies to each part.

Acme has its own 4-cent price throughout the example period. The default change to 6 cents does not replace that eligible customer price. A late October event also uses its historical consumption price, even if it is received in November.

A successful assignment is recorded in `usage_ratings(source, event_id, group_id)`. Its primary key lets one inbox identity contribute to one group exactly once. Foreign keys require both the event and group. The selected price is traceable through `rated_usage_groups.price_version_id`; the rating link does not store a separate amount for each event.

Each rating segment must fit within one UTC month and one applicable price version. The link trigger checks the customer, metric, and month boundaries; it neither selects the price nor checks for a price change inside the interval. The initial rater rejects unsupported crossing segments as visible processing errors. Splitting one receipt across several price groups would require extending the current one-event-to-one-group relationship and defining how to divide units precisely.

## 5. RatedUsageGroup: the rated accounting intermediate

`RatedUsageGroup` answers: **"How much does this compatible consumption cost under one historical price?"** It contains both usage and its financial result. The group lets many measurements from different sandboxes share one rounding boundary.

The SQL table is `rated_usage_groups`. Its unique key is:

```text
(customer_id, price_version_id, usage_month, billing_month)
```

`metric` is also stored and must match the price version. All amounts are USD, so the key has no currency dimension. A different customer, price, original month, or billing month creates a different group. Sandbox is outside this key.

| Column | What it stores |
| --- | --- |
| `group_id` | The stable group identity used by rating links and credit debits. |
| `customer_id`, `price_version_id`, `metric` | The owner and pricing meaning of the group. |
| `usage_month` | The first day of the original consumption month, derived in UTC. |
| `billing_month` | The first day of the invoice period that includes the charge; it cannot precede the original month. |
| `total_units` | The exact sum of counted units. |
| `exact_charge_ticks` | The exact **gross charge before credit**. |
| `booked_charge_cents` | The cumulatively rounded gross projection; exact credit allocation uses ticks. |
| `allocated_credit_ticks` | The exact credit allocated to this group for its future invoice; at most `exact_charge_ticks`. |

The discussion used the conceptual name `gross_charge_ticks` for a group's gross charge. **The actual group column is `exact_charge_ticks`.** The name `gross_charge_ticks` belongs to another table, `monthly_usage`, which sums the gross charges of all groups for a customer's original month.

After 1_000 small measurements, a future worker could create:

```text
customer_id             cyberdyne
metric                  cpu_seconds
price_version_id        cpu-default-2026-10-01
usage_month             2026-10-01
billing_month           2026-10-01
total_units             1_000_000
exact_charge_ticks      5_000_000
booked_charge_cents     5
allocated_credit_ticks  0
```

`price_version_id` refers to the seeded version. The cent value is a rounded gross projection; the migration itself does not calculate it.

Grouping also enforces a correctness rule: adding sandbox or batch to the key would give each one its own rounding boundary, potentially changing the charge for the same total units. Omitting price or months would combine consumption with different accounting meanings.

A group's identity cannot change, but its running totals remain mutable. The database does not yet freeze groups after invoicing. It also does not automatically prove that `total_units` equals the linked events' sum, ticks match the selected price, or cents follow the rounding rule. Future financial writers must maintain and verify these relationships.

## 6. Credits: gross charge, allocated credit, and balance

The gross charge describes the value of consumption. Credit determines how much of that charge is covered by the customer's available funds.

```text
gross usage charge       USD 10.00 = 1_000 cents = 1_000_000_000 ticks
applied credit          -USD  7.00 =  -700 cents =  -700_000_000 ticks
----------------------------------------------------------------------
payable usage            USD  3.00 =   300 cents =   300_000_000 ticks
```

`exact_charge_ticks` remains 1_000_000_000. Replacing it with 300_000_000 would lose the original consumption value. Credit also preserves `total_units` and the historical price.

The current schema represents credit in three places:

| Location | Question answered |
| --- | --- |
| `credit_entries` | Which grant or debit changed the account, and why? |
| `customer_billing_state.credit_balance_ticks` | How much exact credit is available now? |
| `rated_usage_groups.allocated_credit_ticks` | How much exact credit must this group retain for its future invoice? |

The ledger contains `credit_entry_id`, `customer_id`, a stable `operation_id`, optional `group_id`, a signed `amount_ticks`, and `recorded_at`. A grant is positive and has no group; a debit is negative and links to a group owned by the same customer. Zero debits are omitted. Records are append-only, and `(customer_id, operation_id)` is unique so retrying one operation cannot grant or consume credit twice.

The balance is a fast ledger projection. Before allocating credit, a future writer must lock the `customer_billing_state` row and change the balance, ledger, group allocation, and `state_version` together. The database does not calculate the ledger sum or verify agreement between these projections automatically.

### Why the limit uses gross charges

The example above counts USD 10 toward the monthly limit even though the customer pays only USD 3 for usage. `monthly_usage.gross_charge_ticks` retains the charge in the **original UTC consumption month**, before credit and excluding add-ons.

Convert `customer_billing_state.spend_limit_cents` to the same precision. For a configured limit:

```text
reached = gross_charge_ticks >= spend_limit_cents × 1_000_000
```

A USD 8 limit is therefore reached at USD 10 of gross consumption, even when payable usage is only USD 3. An unset limit (`NULL`) means unlimited; a zero limit is reached even with zero usage. Billing continues to accept and account for all measured consumption. Credit pays for usage only; it cannot cover the USD 20 monthly `concurrency_pack`.

### What credit before rounding means

A whole cent of credit converts exactly to ticks. Exact usage can consume less than a cent of that credit:

```text
available credit = 1 cent = 1_000_000 ticks
gross usage = 5_000 ticks
applied credit = min(1_000_000, 5_000) = 5_000 ticks
remaining credit = 995_000 ticks = 0.995 cent
net usage = 0 ticks
```

Before migration 004, `credit_balance_cents`, `credit_entries.amount_cents`, and `allocated_credit_cents` were `bigint` columns. They could represent neither this debit nor the remaining 0.995 cent. A 5_000-tick debit is only a fraction of a cent.

Migration 004 introduces non-negative `credit_balance_ticks` and `allocated_credit_ticks`, plus signed integer `credit_entries.amount_ticks`. It multiplies the original cents by a million and preserves history; incompatible legacy allocations exceeding exact gross charges are rejected atomically. `accounting.AllocateCredit` allocates exact credit to newly processed usage. Allocations follow transaction order under the customer lock; a later grant does not rewrite earlier allocations. Fractional-cent credit remains in the account as ticks.

The historical alternative compatible with the columns before migration 004 instead allocates credit against new cumulative **cent increments** of a group. It preserves exact usage ticks, but operationally computes gross cents first and then allocates cent credit. These are distinct policies. The confirmed current policy allocates exact ticks before rounding; the historical cent alternative below explains the difference.

## 7. Rounding: when and where cents are produced

Rounding applies to the cumulative amount of a compatible group. Raw units and exact ticks retain their values. The rounding rule is a design choice: the assignment requires cent-valued invoices and the example's results, but leaves some boundary cases unspecified.

The shared `RoundCents` calculation uses `half-up` for non-negative amounts, rounding half a cent upward:

```text
round_half_up_cents(ticks) = floor((ticks + 500_000) / 1_000_000)
```

| Exact amount | Result |
| --- | --- |
| 5_000 ticks = 0.005 cent | 0 cents |
| 499_999 ticks = 0.499_999 cent | 0 cents |
| 500_000 ticks = 0.5 cent | 1 cent |
| 1_000_000 ticks = 1 cent | 1 cent |
| 617_283_945 ticks = 617.283_945 cents | 617 cents = USD 6.17 |

The current `InvoiceAmounts` rounds the group's cumulative gross and net amounts. The credit line is the negative difference between those cent values, rather than an independently rounded exact debit. Line totals therefore equal rounded net usage. With 1 cent gross and exactly 0.5 cent of credit, net usage of 0.5 cent rounds to 1 cent; displayed credit is 0 cents, while the ledger retains an exact 500_000-tick debit. A future invoice snapshot must also retain those exact amounts for audit.

### Historical design before migration 004: book only each new cent increment

This section retains the original cent alternative and its column names at that time. The assignment allows credit balances to be queried before invoice issuance. After every event, the earlier design therefore calculates the rounded **total group state**, subtracts previously booked cents, and allocates credit only against the difference:

```text
new_booked_cents = round_half_up_cents(new_exact_charge_ticks)
delta_cents = new_booked_cents - old_booked_charge_cents
credit_debit_cents = min(credit_balance_cents, delta_cents)

booked_charge_cents = new_booked_cents
allocated_credit_cents += credit_debit_cents
credit_balance_cents -= credit_debit_cents
credit_entries.amount_cents = -credit_debit_cents  // only when positive
```

The group grows as follows with 1_000-unit measurements at 5 cents per million:

| Event count | `total_units` | `exact_charge_ticks` | Cumulative `booked_charge_cents` |
| --- | --- | --- | --- |
| 1 | 1_000 | 5_000 | 0 |
| 100 | 100_000 | 500_000 | 1 |
| 200 | 200_000 | 1_000_000 | 1 |
| 1_000 | 1_000_000 | 5_000_000 | 5 |

Exact values remain available when the running cent projection changes. Between the 100th and 200th events, one cent has already been booked, so it is not added again. All cent increments sum to the rounded result for the entire group.

This preserves the same gross charge under different splits of **the same open group**. It does not prove that credit allocations are independent of the order of different groups, late input, or new grants. The earlier design uses credit available at processing time and does not retroactively change historical allocations when later grants arrive; that policy requires explicit worker tests.

The invoice then snapshots already booked gross cents and allocated credit. Closing does not allocate credit again or round individual events again. Operationally, this historical alternative converts gross charges to cents before allocating credit. The diagram's exact-credit-before-rounding flow and the historical cent columns must be read with that distinction in mind.

## 8. InvoiceLine: what appears on a particular invoice

`RatedUsageGroup` retains the rated accounting intermediate. `InvoiceLine` preserves what appeared on a specific issued invoice. A group can grow during an open period; an issued line must retain its values when later measurements, prices, or addresses change.

The `invoice_lines` table does not exist yet. The [logical ERD](../diagrams/data-model/data-model.mmd) proposes:

| Proposed column | Meaning |
| --- | --- |
| `invoice_line_id`, `invoice_id` | The line identity and its invoice reference. |
| `group_id` | An optional reference for a usage or credit line. |
| `subscription_id` | An optional reference for an add-on line. |
| `description_snapshot` | An immutable description, including the historical rate or original month where needed. |
| `amount_cents` | The final signed amount: usage and add-ons are positive; allocated credit is negative. |

Future closing must also preserve the required quantity, metric, price, months, and credit details. Issued amounts and descriptions cannot be reconstructed later from mutable groups or catalogs. Snapshot the required data and close the groups in the same transaction.

The current policy derives usage and credit lines with `InvoiceAmounts(exact_charge_ticks, allocated_credit_ticks)`. Credit can be presented as a combined line by summing already calculated cent differences, but its origin and exact ticks must remain traceable. Combining lines cannot introduce another rounding step.

Add-on charges reach the invoice through a separate path: `addon_subscriptions.monthly_price_cents` retains the purchase price. It requires neither resource rating nor usage credit and is already in whole cents. An invoice can therefore contain lines that did not originate as usage events.

The intended usage audit path is:

```text
invoice line -> frozen rated group -> usage_ratings -> usage_inbox
                                 -> price_versions
                                 -> credit_entries
```

## 9. Invoice: an immutable monthly document

The `invoices` table also does not exist yet. The proposal contains `invoice_id`, `customer_id`, `billing_month_utc`, `customer_sequence`, `address_snapshot`, `total_cents`, and `issued_at_utc`. Lines reference this invoice.

The future schema must enforce one invoice per `(customer_id, billing_month_utc)` and a unique `(customer_id, customer_sequence)`. Numbering is per customer: `ACME-0001`, `ACME-0002`; Cyberdyne's numbers do not advance Acme's sequence. The conceptual `last_invoice_number` from the ERD is absent from the current `customer_billing_state`, so numbering still needs implementation.

`total_cents` is the exact integer sum of signed lines. The address comes from `customers` at issuance and is stored in a snapshot. Later customer changes must preserve the issued document.

### Example: complete assignment invoices

Pure Go calculation tests verify the following results. The future runtime and invoice writer must also reproduce them through public APIs:

| Line | ACME-0001, October | ACME-0002, November | CYBERDYNE-0001, October |
| --- | --- | --- | --- |
| Acme usage, 4 cents/million | USD 12.00 | USD 4.00 | — |
| Late October Acme usage, 4 cents/million | — | USD 2.00 | — |
| Usage at 5 cents/million | — | — | USD 6.17 |
| Usage at 6 cents/million | — | — | USD 12.00 |
| `concurrency_pack` | USD 20.00 | USD 20.00 | — |
| Allocated credit | −USD 12.00 | −USD 6.00 | USD 0.00 |
| **Total** | **USD 20.00** | **USD 20.00** | **USD 18.17** |

Acme receives a USD 25 grant (`amount_ticks = 2_500_000_000`). October's 300 million units at 4 cents per million produce `1_200_000_000 ticks`, or 1_200 cents. Allocating USD 12 of credit leaves USD 13. The invoice contains usage of `1_200`, credit of `-1_200`, and an add-on of `2_000`, totaling `2_000` cents.

November's invoice includes USD 2 of late October usage and USD 4 of November usage. Their groups have separate original months; together they consume USD 6 of credit. The balance falls from USD 13 to USD 7. The add-on contributes another USD 20 and cannot consume credit. The invoice again totals USD 20. Issuance and retries must not deduct credit a second time.

Cyberdyne's first rate gives `123_456_789 × 5 = 617_283_945 ticks`, rounded to 617 cents. The second rate creates another group with `200_000_000 × 6 = 1_200_000_000 ticks`, or 1_200 cents. The invoice totals 1_817 cents. Exact gross October usage is `1_817_283_945 ticks`, above the USD 15 limit (`1_500_000_000 ticks`). November's limit is evaluated against November's gross state; October's invoice retains its issued values.

## 10. Late usage: why there are two distinct periods

Acme consumes 50 million units on 30 October, but the platform delivers them after October's invoice has been issued. Rating still uses October's customer-specific price of 4 cents per million:

```text
50_000_000 × 4 = 200_000_000 ticks = USD 2.00
```

The new group has:

```text
usage_month    2026-10-01
billing_month  2026-11-01
```

November's 100 million units form another group:

```text
usage_month    2026-11-01
billing_month  2026-11-01
exact_charge_ticks  400_000_000
```

November's invoice includes USD 2 for October and USD 4 for November, while the gross projection for limits assigns the USD 2 back to October. After the late charge, Acme's gross usage is USD 14 for October and USD 4 for November. The issued October invoice remains immutable.

A single "month" column would lose this distinction. A rated group describes the origin and cost of usage; an invoice line describes the particular issued document that bills it.

Closing still needs a stable boundary of accepted customer events. It waits for their processing without holding the lock needed by the worker, then atomically freezes groups and creates the invoice and its lines under the customer lock. An unresolved error before this boundary must block closing. Later input is routed to an appropriate open period. The current schema stores both months; closing and routing require application implementation.

## 11. What must stay atomic during processing

In one transaction, the worker must assign the event, update the group and original-month gross state, apply any credit debit and balance change, update `state_version`, and mark inbox `processed_at`. Otherwise a crash could consume credit without completing the event, or a retry could apply the financial effect twice.

Unique `usage_ratings` links are a necessary safeguard, but do not alone prove exactly-once financial effects. Writes must share a transaction and customer lock. On error, input remains traceable; missing customers, metrics, or prices must fail visibly rather than create free consumption.

The confirmed policy allocates sub-cent ticks before rounding. Pure Go tests verify the amounts above, half-cent boundaries, split usage, price and UTC boundaries, later grants, late routing, and overflow. The worker still needs transactional integration and verification of retries, concurrency, and process failure. Closing additionally needs immutable snapshots, idempotent numbering, and agreement between line totals and invoice totals. These are remaining implementation tasks; the document and diagram distinguish them from implemented behavior.
