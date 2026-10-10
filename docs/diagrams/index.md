# Published diagrams

Start with usage to invoice, the implemented schema, and option C.
Alternatives and earlier proposals are labelled separately from current behavior.

| Diagram | Purpose and status | Mermaid | PNG |
| --- | --- | --- | --- |
| Usage to invoice | Implemented receipt, exact rating, credit, rounding, and invoicing. | [Source](usage-to-invoice/usage-to-invoice.mmd) | [Preview](usage-to-invoice/usage-to-invoice.png) |
| Implemented PostgreSQL schema | Implemented tables and relationships through migration 009. | [Source](implemented-data-model/implemented-data-model.mmd) | [Preview](implemented-data-model/implemented-data-model.png) |
| Price activation | Implemented activation boundary and controlled historical recovery. | [Source](price-version-activation/price-version-activation.mmd) | [Preview](price-version-activation/price-version-activation.png) |
| Usage accounting | Implemented per-receipt atomic accounting and quarantine. | [Source](usage-worker-accounting/usage-worker-accounting.mmd) | [Preview](usage-worker-accounting/usage-worker-accounting.png) |
| Invoice issuance | Implemented closing from processed groups and immutable publication. | [Source](invoice-issuance/invoice-issuance.mmd) | [Preview](invoice-issuance/invoice-issuance.png) |
| Closing overview | Implemented account locking, month order, presentation, and atomic publication. | [Source](closing-workflow/closing-workflow.mmd) | [Preview](closing-workflow/closing-workflow.png) |
| Option A | Alternative: synchronous Go accounting with SQLite. | [Source](option-a-components/option-a-components.mmd) | [Preview](option-a-components/option-a-components.png) |
| Option B | Alternative: synchronous Go accounting with PostgreSQL. | [Source](option-b-components/option-b-components.mmd) | [Preview](option-b-components/option-b-components.png) |
| Option C | Selected and implemented: PostgreSQL inbox and separate Go worker. | [Source](option-c-components/option-c-components.mmd) | [Preview](option-c-components/option-c-components.png) |
| Option D | Proposal: Kafka ingress, archive, aggregation, and separate accounting. | [Source](option-d-components/option-d-components.mmd) | [Preview](option-d-components/option-d-components.png) |
| Logical data model | Earlier design proposal; use the implemented schema for actual tables. | [Source](data-model/data-model.mmd) | [Preview](data-model/data-model.png) |

Detailed explanations: [option C](option-c-components/README.md) and
[invoice issuance](invoice-issuance/README.md). Each diagram has one editable
English Mermaid source and its corresponding PNG; documentation rendering
tooling remains local and ignored. The full local catalog stays in the ignored
`docs/diagrams/README.md`.
