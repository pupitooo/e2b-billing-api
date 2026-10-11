# E2B billing documentation

The project implements the assignment in Go and PostgreSQL. Run `make docs`
for this searchable portal; its guides also remain readable in the repository.

## Quick links

| I want to… | Start here |
| --- | --- |
| Run the project | [Local setup](guides/local-development.md) |
| Try billing in the browser | [Demo guide](guides/demo-console.md), [Demo console](/demo/) |
| Connect to the database | [Database](guides/database-operations.md#database-connection), [Database browser](/database/) |
| Run tests | [Tests](guides/testing.md) |
| Send an API request | [API reference](/reference/) with the embedded request client |
| Understand the billing flow | [Usage to invoice](architecture/usage-to-invoice.md) |

## Browse by topic

These categories and page names match the navigation menu.

| Category | Pages |
| --- | --- |
| Get started | [Local setup](guides/local-development.md), [Demo guide](guides/demo-console.md), [Demo console](/demo/) |
| Operations | [Database](guides/database-operations.md), [Migrations](guides/migrations.md), [Configuration](guides/runtime-configuration.md), [Troubleshooting](guides/troubleshooting.md), [Recovery](guides/accounting-recovery.md), [Database browser](/database/) |
| API | [API guide](guides/api-reference.md), [API reference](/reference/), [OpenAPI specification](api/openapi.yaml) |
| Billing | [Financial rules](architecture/accounting-rules.md), [Data model](architecture/billing-model.md), [Usage to invoice](architecture/usage-to-invoice.md), [Invoice creation](diagrams/invoice-issuance/README.md) |
| Simulator | [Transport scenarios](simulator/transport-scenarios.md), [Billing scenarios](simulator/billing-scenarios.md), [Scenario format](simulator/scenario-style.md) |
| Testing | [Tests](guides/testing.md), [Test quality](guides/test-mutation-audit.md), [Acceptance](guides/final-acceptance-testing.md) |
| Development | [Conventions](guides/development.md), [Documentation](guides/documentation.md) |
| Architecture | [Overview](architecture/submission-overview.md), [System contracts](architecture/system-contracts.md), [Decisions](architecture/decisions.md), [Design options](brainstorming/architecture-options.md), [Invoice scheduling proposals](brainstorming/invoice-scheduling.md), [Capacity](architecture/capacity-and-measurements.md), [Diagrams](diagrams/index.md), [Components](diagrams/option-c-components/README.md) |

Use the [isolated assignment setup](simulator/billing-scenarios.md#start-an-isolated-assignment-environment)
to reproduce billing scenarios without changing development accounts.
The [acceptance overview](guides/final-acceptance-testing.md) links the four parts
of the complete verification procedure in execution order.

The [system contract register](architecture/system-contracts.md) distinguishes
assignment requirements from implementation policies and links their detailed contracts.
Design proposals and public-product context do not expand assignment requirements.
Only explicitly selected public files appear in this portal; ignored assignment
materials and local working documents remain outside it.
