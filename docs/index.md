# E2B billing documentation

The project implements the assignment in Go and PostgreSQL. The hosted edition
contains guides, diagrams, search, and a read-only API reference. Run `make docs`
for the local portal with API requests, demo console, and database browser.
The guides also remain readable in the repository. See
[documentation publication](guides/documentation.md#github-pages-publication).

## Quick links

| I want to… | Start here |
| --- | --- |
| Run the project | [Local setup](guides/local-development.md) |
| Try billing in the browser | [Demo guide](guides/demo-console.md), [Demo console](/demo/) |
| Connect to the database | [Database](guides/database-operations.md#database-connection), [Database browser](/database/) |
| Run tests | [Tests](guides/testing.md) |
| Read the API contract | [API reference](/reference/); the local portal also provides a request client |
| Understand the billing flow | [Usage to invoice](architecture/usage-to-invoice.md), [processing sequences and edge cases](architecture/processing-sequences.md) |
| Understand competing operations | [Concurrency and locking](architecture/concurrency-and-locking.md) |
| Review selected and open contracts | [Implemented contracts](architecture/implemented-contracts.md), [proposed contracts](architecture/proposed-contracts.md) |
| Review limitations and investigation plans | [Limitations](architecture/limitations.md) |

## Browse by topic

These categories and page names match the navigation menu.

| Category | Pages |
| --- | --- |
| Get started | [Local setup](guides/local-development.md), [Demo guide](guides/demo-console.md), [Demo console](/demo/) |
| Operations | [Database](guides/database-operations.md), [Migrations](guides/migrations.md), [Configuration](guides/runtime-configuration.md), [Troubleshooting](guides/troubleshooting.md), [Recovery](guides/accounting-recovery.md), [Database browser](/database/) |
| API | [API guide](guides/api-reference.md), [API reference](/reference/), [OpenAPI specification](api/openapi.yaml) |
| Billing | [Vocabulary](guides/billing-vocabulary.md), [Financial rules](architecture/accounting-rules.md), [Data model](architecture/billing-model.md), [Usage to invoice](architecture/usage-to-invoice.md), [Processing sequences](architecture/processing-sequences.md), [Concurrency and locking](architecture/concurrency-and-locking.md), [Invoice creation](diagrams/invoice-issuance/README.md) |
| Simulator | [Transport scenarios](simulator/transport-scenarios.md), [Billing scenarios](simulator/billing-scenarios.md), [Scenario format](simulator/scenario-style.md) |
| Testing | [Tests](guides/testing.md), [Test quality](guides/test-mutation-audit.md), [Acceptance](guides/final-acceptance-testing.md) |
| Development | [Conventions](guides/development.md), [Documentation](guides/documentation.md) |
| Architecture | [Overview](architecture/submission-overview.md), [Limitations](architecture/limitations.md), [Decisions](architecture/decisions.md), [Design options](brainstorming/architecture-options.md), [Invoice scheduling proposals](brainstorming/invoice-scheduling.md), [Capacity](architecture/capacity-and-measurements.md), [Diagrams](diagrams/index.md), [Components](diagrams/option-c-components/README.md) |
| Architecture → System contracts | [Implemented contracts](architecture/implemented-contracts.md), [Proposed contracts](architecture/proposed-contracts.md) |

Use the [isolated assignment setup](simulator/billing-scenarios.md#start-an-isolated-assignment-environment)
to reproduce billing scenarios without changing development accounts.
The [acceptance overview](guides/final-acceptance-testing.md) links the four parts
of the complete verification procedure in execution order.

[Implemented contracts](architecture/implemented-contracts.md) are grouped by
assignment requirements, additions and refinements.
[Proposed contracts](architecture/proposed-contracts.md) contain pending requirements
and open decisions.
Design proposals and public-product context do not expand assignment requirements.
Only explicitly selected public files appear in this portal; ignored assignment
materials and local working documents remain outside it.

[Delayed billing scenarios](simulator/delayed-billing.md) combine physical platform lag, missed manual invoices, immutable snapshots, credit and add-on lifecycle changes.

Backend verification: [E2E scenarios](guides/e2e-testing.md) and
[manual checkpoint walkthrough](guides/e2e-walkthrough.md).
