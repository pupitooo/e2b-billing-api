# Implemented contracts

## Contracts required by the assignment

These rules come from the assignment's domain model, business rules and worked
example. The tables below separate those requirements from added contracts and
selected refinements. Detailed rules and API behavior remain in the linked guides.

| Contract | Assignment requirement | Implementation detail |
| --- | --- | --- |
| Customer billing data and callers | Bill customers with a country and billing address. E2B grants credit; customers purchase add-ons and set limits; the platform submits usage and reads reached status. | [Interfaces and ownership](submission-overview.md#interfaces-and-ownership). |
| Distributed usage delivery | Account for repeated requests, delayed input and platform unavailability without changing the correct billed quantities. | [usage_inbox row and accounting](submission-overview.md#usage_inbox-row-and-accounting). Concrete event identities and acknowledgment semantics are added contracts below. |
| Historical prices and customer overrides | Rate consumption at its consumption-time price; the customer's own price takes precedence over the default. Support price changes over time and preserve the seed values. | [Historical pricing](accounting-rules.md#historical-prices-and-groups). |
| Credit | E2B grants credit; customers cannot buy it themselves. Credit pays usage only, never add-ons. Return the remaining balance. | [Money and credit](accounting-rules.md#exact-money-and-credit). Precision and allocation order are refinements below. |
| Monthly add-on fees | Charge the full fixed price for every active month, beginning with the purchase month. | [Add-on rules](accounting-rules.md#utc-months-limits-add-ons-and-late-usage). Cancellation and reactivation are added contracts below. |
| Gross monthly spend limits | Count current-month usage before credit and exclude add-ons. Account for all measured usage beyond the limit. Raising the limit or starting a new month can put the customer below it again. The platform owns its reaction. | [Limit rules](accounting-rules.md#utc-months-limits-add-ons-and-late-usage). Equality and zero/unset behavior are refinements below. |
| Monthly invoices and numbering | Issue one monthly invoice per customer for whole UTC calendar months, in USD cents, with usage, add-ons and used credit. Number invoices separately for each customer, such as `ACME-0001`, `ACME-0002`. Preserve issued financial history. | [Invoice model](usage-to-invoice.md#9-invoice-an-immutable-monthly-document). Automatic invocation is a [pending requirement](proposed-contracts.md#automatic-invoice-issuance). |
| Late consumption | The worked example bills late October consumption on the following invoice at its original price; it preserves the earlier invoice. | [Late usage example](usage-to-invoice.md#9-invoice-an-immutable-monthly-document). General routing across further closed months is a refinement below. |
| Simulator and worked results | Provide a platform substitute and reproduce the assignment's invoices and credit balances with the original input. | [Public assignment workflow](../simulator/billing-scenarios.md#run-the-complete-assignment): invoices USD 20.00, USD 20.00 and USD 18.17; Acme credit USD 13.00 and USD 7.00 at the specified checkpoints. |

## Added contracts

The assignment leaves these protocols, identities, operating controls and extra
features to the design. They are implemented choices, rather than rules explicitly
prescribed by the assignment.

| Contract | Implemented behavior and detail | Origin |
| --- | --- | --- |
| Implementation stack | Go is the fixed project language; PostgreSQL owns usage_inbox and financial storage. The assignment leaves language, storage and tools open. [Tools and transaction boundaries](submission-overview.md#tools-and-transaction-boundaries). | Added project requirement for Go; selected storage architecture. |
| Durable usage_inbox row and event identity | The batch envelope contains only `events`, with no batch identity. `(source, event_id)` identifies an increment across customers, sandboxes, processes and restarts. Acknowledge committed input; identical retries preserve arrival time, changed content conflicts, and the sender retains unacknowledged persisted input. [Event contract](usage-to-invoice.md#3-usage-events-what-was-consumed). | Delivery protocol and inbox policy. |
| API and worker lifecycle | Separate processes and containers, independent restarts, shared PostgreSQL capacity. Each process constructs one PostgreSQL store with shared connection, clock, and rollback settings. The API requires the complete store at startup and always registers all implemented endpoints. [Runtime boundaries](submission-overview.md#runtime-boundaries), [option C](../brainstorming/architecture-options.md#why-option-c-was-selected). | Architecture choice. |
| Business clock | API and worker select one provider from optional `E2B_DEMO_NOW`; unset samples real time, configured fixes UTC business time. HTTP callers cannot set it. The docs portal serves startup metadata for the GUI; operational timers remain real. [Clock configuration](../guides/runtime-configuration.md#shared-business-clock). | Demo configuration and system time boundary. |
| Generated resource IDs and command retries | Billing generates price IDs and derives readable new subscription IDs as `subscription/<customer_id>/<addon_name>` with each component percent-encoded. Prices, add-on purchases and cancellations, invoice catch-up, credit grants, and spend-limit changes require JSON `idempotency_key`; these keyed financial commands store their keys and results in `api_idempotency_operations` in the same transaction as their financial effects. Caller keys never become internal resource IDs. Missing or invalid keys return `422`; duplicate fields or wrong types return `400`; changed content conflicts. Existing IDs and financial history remain unchanged. [Command retries](../guides/api-reference.md#command-retries). | System API identity policy. |
| Accounting resource IDs | New rated groups use `grp_1`, `grp_2`, etc.; new credit ledger entries use `crd_1`, `crd_2`, etc. Each namespace has a persistent, noncycling PostgreSQL sequence starting at one. Allocation is safe across API and worker instances without a collision lookup. Rollback can leave gaps; existing IDs, references and invoices are preserved. [Identity rules](accounting-rules.md#accounting-resource-identities). | System storage identity policy. |
| Credit audit time | Grant requests and responses contain no `recorded_at`. The backend writes its UTC clock to the ledger and command history once; retries preserve that audit instant. [Credit contract](accounting-rules.md#credit-audit-time-and-add-on-purchase-dates). | System audit policy. |
| Catalog recovery | Provision dated prices before metering. Missing prices quarantine without financial effects and report P0; repair through controlled SQL, then explicitly release. [Recovery contract](accounting-rules.md#catalog-provisioning-and-p0-recovery). | System provisioning policy. |
| Add-on lifecycle | Cancellation appends an inclusive final charged month; later nonoverlapping purchases snapshot the current price and append their UTC purchase date to the base resource ID. Closing and retries preserve historical purchases and termination records. [Lifecycle](accounting-rules.md#utc-months-limits-add-ons-and-late-usage). | Cancellation and reactivation are added features; full active-month fees follow the assignment. |
| Missed manual invoices | Explicit catch-up issues every missing completed month atomically, including empty and active add-on months; at most 12 per command. A successful key freezes the original returned range and all retries. [Recovery](../simulator/delayed-billing.md#missed-manual-invoice). | System recovery and transaction-size policy; automatic invocation is not implemented. |
| Supported timestamps | After UTC conversion, years 1000 through 9999 with at most microsecond precision. [Event validation](usage-to-invoice.md#3-usage-events-what-was-consumed). | System parsing boundaries. |
| Financial atomicity | Serialize financial writes through the customer account lock. Commit each usage_inbox row with its rating, credit, ledger, gross projection and completion. Publish closure, frozen groups, snapshot and number atomically; retries preserve the committed result. [Transaction contract](accounting-rules.md#transaction-and-closing-contract). | Transaction and concurrency policy. |

## Refined assignment contracts

These choices define validation, boundary cases or financial outcomes left
unspecified by the assignment. Each row shows the original rule and the selected
refinement, including restrictions imposed by the current implementation.

| Contract | Assignment rule | Implemented refinement and effect |
| --- | --- | --- |
| usage_inbox row segmentation | Price usage at its consumption time over UTC calendar months. | One usage_inbox row must fit one applicable price version and one UTC month; crossing intervals are rejected. The platform must supply supported segments. [Pricing boundaries](accounting-rules.md#historical-prices-and-groups). |
| Price insertion | Allow prices to change over time. | `effective_from` is a required `YYYY-MM-DD` date, stored at midnight UTC. New dates must be today or later according to the server clock after the catalog lock; today must preserve rated history. Past dates return `422`, history conflicts `409`, and identical retries retain the original price. Historical catalog repair uses controlled SQL. [Insertion rules](accounting-rules.md#price-insertion-contract). |
| Add-on purchase date and price | Charge full monthly fees beginning with the purchase month. | `purchased_at` is a required UTC calendar date stored at midnight and returned as a date. Open historical and future dates are allowed; new purchases in or before a closed month conflict. A purchase snapshots the catalog price for its recurring fees. [Purchase rules](accounting-rules.md#credit-audit-time-and-add-on-purchase-dates). |
| Exact credit and allocation order | Credit pays usage only, and the balance must match the worked example. | Whole microcents preserve sub-cent amounts; 1 cent = 1_000_000 microcents. Allocate credit before rounding, in serialized accounting order. Later grants do not rewrite previous allocations. [Money and credit](accounting-rules.md#exact-money-and-credit). |
| Invoice rounding | Return USD cents and the example's totals. | Round cumulative groups half-up; displayed credit is rounded gross minus rounded net. Zero-cent lines and zero-total invoices keep complete units and exact audit amounts. No minimum charge or remainder carry between groups or invoices. [Presentation](accounting-rules.md#rounding-and-invoice-presentation). |
| Reached-limit boundary | Tell the platform whether the monthly gross usage limit has been reached. | Compare exact original-month gross usage with `>=`. An unset limit is unlimited; zero is reached even with no usage. Status includes committed accounting and can lag accepted or platform-buffered input. [Limit rules](accounting-rules.md#utc-months-limits-add-ons-and-late-usage), [freshness limitation](limitations.md#l1-delayed-spend-limit-status). |
| Completed months and invoice identity | Issue monthly invoices for whole UTC calendar months and preserve issued financial history. | Close only ended months. Ongoing or future months return `422` without publication. `(customer_id, billing_month)` identifies one immutable buyer/financial snapshot; retries retain its number and values. [Completed-month guard](accounting-rules.md#completed-month-requirement). |
| Chronological closing | Bill every active add-on month and preserve late usage. | The first closing cannot skip earlier known usage or active add-on months, including subscriptions with no usage. Every later new invoice requires the preceding month closed, including empty months; skipped obligations conflict. [Closing order](accounting-rules.md#transaction-and-closing-contract). |
| Processed-usage cutoff | Combine usage, add-ons and credit in the monthly invoice; the example permits late delivery. | Close groups already processed under the account lock without draining or waiting on pending/errors. Even input accepted before closing can appear later. [Closing contract](accounting-rules.md#transaction-and-closing-contract), [deferral impact](limitations.md#l2-accepted-usage-on-a-later-invoice). |
| General late-usage routing | Bill late October consumption later at its original price without rewriting October's invoice. | Keep original-month prices, gross spend and audit; route consumption from closed or skipped months after the latest closure and at or after its UTC arrival month. This extends the example to further closures. [Routing](accounting-rules.md#utc-months-limits-add-ons-and-late-usage). |

### Changes to assignment requirements

No adopted replacement of an assignment requirement is documented. The refinements
above select unspecified behavior or constrain supported inputs. Cancellation and
catch-up add functionality. Previous implementation alternatives, such as draining
pending input before closing, are described in the design material; they are not
additional active contracts. A proposed change that conflicts with the assignment
must identify and resolve that conflict before adoption.

## Invocation and guarantee boundaries

Invoice issuance is implemented through explicit API commands. The accounting
worker does not schedule invoices. Safe closing and manual catch-up are existing
building blocks; [automatic issuance](proposed-contracts.md#automatic-invoice-issuance)
is required but unimplemented.

Durable acknowledgment protects input committed to the billing database. Sender
retries protect persisted measurements while their storage survives. Recovery of
never-recorded measurements or destruction of the only storage copy requires a
separate contract. Production capacity and numeric freshness, recovery-time or
data-loss targets are not established. See the [limitations](limitations.md),
[capacity assessment](capacity-and-measurements.md) and [open contracts](proposed-contracts.md).

## Existing verification

Backend verification uses [Go E2E scenarios](../guides/e2e-testing.md) against
separate application services with trusted configuration clock changes. The
[manual walkthrough](../guides/e2e-walkthrough.md) preserves inspectable operator
checkpoints. These test controls do not add public clock parameters or change
financial runtime contracts.
