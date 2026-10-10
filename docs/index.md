# E2B billing documentation

The project implements the assignment in Go and PostgreSQL. Read the
[architecture and assignment coverage](architecture/submission-overview.md) for
responsibilities and current limits, then follow a measurement through the
[usage-to-invoice walkthrough](architecture/usage-to-invoice.md).

Run `make docs` for this searchable portal. The **API reference** navigation entry
opens Scalar with its embedded request client on the same browser origin.
The source Markdown also remains readable directly in the repository.

| Topic | Guides |
| --- | --- |
| Start and operate | [Local development](guides/local-development.md), [runtime configuration](guides/runtime-configuration.md), [database operations](guides/database-operations.md) |
| Explore HTTP interfaces | [API endpoints and callers](guides/api-reference.md#api-endpoints-and-callers), [API reference guide](guides/api-reference.md), [OpenAPI specification](api/openapi.yaml) |
| Reproduce usage and billing | [Transport simulator](simulator/transport-scenarios.md), [public billing scenarios](simulator/billing-scenarios.md), [scenario style](simulator/scenario-style.md) |
| Verify and contribute | [Tests and filters](guides/testing.md), [test quality and mutation audit](guides/test-mutation-audit.md), [acceptance procedure](guides/final-acceptance-testing.md), [development conventions](guides/development.md) |
| Understand accounting | [Financial rules](architecture/accounting-rules.md), [billing model and schema](architecture/billing-model.md), [usage to invoice](architecture/usage-to-invoice.md) |
| Compare and measure | [Architecture options](brainstorming/architecture-options.md), [capacity and local measurements](architecture/capacity-and-measurements.md), [published diagrams](diagrams/index.md) |
| Investigate failures | [Accounting quarantine and historical-price recovery](guides/accounting-recovery.md) |
| Maintain documentation | [Publication, preview, checks, and service upgrade](guides/documentation.md) |

The [root README](../README.md#system-contracts) holds the system contract register.
Design proposals and public-product context do not expand assignment requirements.
Only explicitly selected public files appear in this portal; ignored assignment
materials and local working documents remain outside it.
