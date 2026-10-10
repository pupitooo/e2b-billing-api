# E2B Billing API

## 🚧 ⚠️ DISCLAIMER — Proof of concept 🚧

This application is a proof of concept for the assignment and requires further
work before production deployment. My goal was to implement a broad billing
workflow, prioritizing the core product logic: reliable usage delivery, historical
pricing, credit allocation, add-on billing, spend-limit reporting, and consistent
monthly invoices, including retries and late usage.

Some supporting interfaces for testing and configuration are provisional. An
internal CRM and other consuming systems are not implemented; their requirements
would help define the operational workflows and acceptance criteria these
interfaces still need. These areas require further specification, review, and
validation before production use.

The implementation makes the main assignment concept testable. Its limitations
and the work needed to take it into production can be discussed during the interview.

---

## Overview

Billing service for the E2B assignment, built with Go and PostgreSQL. It implements
durable usage receipt, transactional accounting, historical prices, usage-only
credit, recurring add-ons, gross spend limits, and immutable monthly invoices.
A separate, restartable Go simulator exercises the public interfaces.

## Architecture and implementation status

The selected [option C architecture](docs/brainstorming/architecture-options.md#why-option-c-was-selected)
uses an HTTP API, PostgreSQL inbox, and an independent accounting worker.

![Option C: building blocks and interfaces](docs/diagrams/option-c-components/option-c-components.png)

[Editable Mermaid source](docs/diagrams/option-c-components/option-c-components.mmd) ·
[Architecture and assignment coverage](docs/architecture/submission-overview.md) ·
[Diagram explanation](docs/diagrams/option-c-components/README.md).

The API commits usage before returning `202`; the worker commits each receipt's
financial effects and completion together. Invoice closing snapshots already
processed usage. See the [usage-to-invoice walkthrough](docs/architecture/usage-to-invoice.md)
and [implemented schema](docs/architecture/billing-model.md#implemented-postgresql-schema-erd-migrations-001012).

Current limits: no authentication, automatic invoice scheduling or delivery;
receipts must fit one UTC month and applicable price version. Retained local
volumes cover restarts, not disk loss. Production capacity has not been established.

## Quick start

Requirements: Docker with Docker Compose and Make. Go and PostgreSQL clients run
inside containers. Run commands from the repository root.

```sh
git clone https://github.com/pupitooo/e2b-billing-api.git
cd e2b-billing-api
# Optional: copy .env.example to .env to change local ports or settings.
make up SERVICE=postgres
make migrate
make up
```

Migration is explicit, including on existing volumes; startup never applies it.
The simulator container waits for `make simulate` before producing usage.

| Service | Default browser address |
| --- | --- |
| API health | [http://127.0.0.1:8081/healthz](http://127.0.0.1:8081/healthz) |
| Documentation portal | [http://127.0.0.1:8082/](http://127.0.0.1:8082/) |
| Scalar API reference and request client | [http://127.0.0.1:8082/reference/](http://127.0.0.1:8082/reference/) |

Use [.env.example](.env.example) for configuration and `make links` for actual
addresses. The `docs` container serves the guides and proxies the reference;
`api-docs` serves Scalar internally. `make docs` starts the portal and API.
`make api-docs` starts the same services and prints the reference link.

See [local development](docs/guides/local-development.md) for service operations,
[runtime configuration](docs/guides/runtime-configuration.md) for API and worker
budgets, and [documentation maintenance](docs/guides/documentation.md) for live
preview and the upgrade from the former Scalar-only `docs` service.

## Try the API and simulator

Send a measurement to the seeded Acme account:

```sh
curl -i http://127.0.0.1:8081/usage/batches \
  -H 'Content-Type: application/json' \
  --data '{
    "events": [{
      "schema_version": 1,
      "source": "readme-example",
      "event_id": "acme-cpu-000001",
      "customer_id": "acme",
      "sandbox_id": "acme-sandbox-001",
      "metric": "cpu_seconds",
      "period_start": "2026-10-10T12:00:00Z",
      "period_end": "2026-10-10T13:00:00Z",
      "units": 100000000
    }]
  }'
```

A valid request returns `202` with `{"status":"accepted"}`. Keep `(source,
event_id)` and content unchanged on retries; changed content returns `409`.
Acceptance confirms durable receipt; financial processing is asynchronous.
See the [OpenAPI specification](docs/api/openapi.yaml) for all financial endpoints,
validation, error responses, and safe retries after a lost response or `503`.
The [API endpoint diagram](docs/diagrams/api-endpoints/api-endpoints.png) maps
methods and paths to their callers; it also appears in Scalar's introduction
and the [API guide](docs/guides/api-reference.md#api-endpoints-and-callers).

Run the transport scenario one step at a time:

```sh
make simulate SCENARIO=assignment MODE=step
make simulate ACTION=status
```

This creates additional usage. Use an isolated environment for independent
experiments. The [transport simulator guide](docs/simulator/transport-scenarios.md)
covers generation, delivery, replay, barriers, and failures.

The [public billing scenarios](docs/simulator/billing-scenarios.md) exercise the
complete assignment with literal invoices and balances. Run them against fresh
seeded accounts with the API and worker running. Their fixed October/November
2026 invoices require the server clock to have reached `2026-12-01T00:00:00Z`;
private-schema integration tests declare their own clocks for earlier verification.

```sh
make simulate SCENARIO=billing-assignment STATE=/state/billing-assignment.json
```

## Database and service operations

Default PostgreSQL settings: host `127.0.0.1`, port `5432`, database `e2b_billing`,
user `e2b`, development password `e2b_local_dev`, session time zone UTC. Inside
Compose, use host `postgres`. The role password is initialized only on an empty
volume; changing `.env` does not update an existing role.

```sh
make psql
make migrate
make migration-status
```

[Database operations](docs/guides/database-operations.md) cover connection strings,
inspection queries, migration guarantees, and upgrade procedures.

| Command | Purpose |
| --- | --- |
| `make ps` / `make links` | Inspect services and browser addresses. |
| `make logs SERVICE=worker` | Inspect accounting and quarantine errors. |
| `make restart SERVICE=worker` | Restart accounting independently of the API. |
| `make stop` | Stop services while retaining containers and data. |
| `make down` | Remove containers and the network while retaining volumes. |
| `make services` / `make help` | List services or all supported commands. |

Investigate unsupported receipts before releasing them. Missing valid prices are
P0 catalog incidents; see the complete [accounting recovery procedure](docs/guides/accounting-recovery.md).
Adding a price or replaying usage does not clear a processing error.

## Tests and development

```sh
make install-hooks
make fmt
make check
make up SERVICE=postgres
make test
make docs-check
```

`make check` verifies Go formatting, whitespace, and static
analysis. `make test` runs SQL integrity in UTC and `Asia/Shanghai`, Go package,
repository, HTTP, and worker lifecycle tests. `make docs-check` validates the
published documentation, links, and assets with a strict MkDocs build.

Use named constants for domain rules, schema/checkpoint versions, protocol
limits, retry policy, operational defaults, and parsing boundaries. Equal values
with different meanings need separate names; use standard-library constants where
available. Keep ordinary zero values, indexing, counters, and literal test inputs
and expectations readable. Review constant names manually; formatting does not
enforce this rule. Group decimal Go literals and Markdown numbers with five or
more digits in threes (`10_000`); calendar years and copyable language examples
retain their required syntax. Review numeric grouping manually; formatting and
automated checks do not enforce it.

See [development conventions](docs/guides/development.md),
[test suites and filters](docs/guides/testing.md), and the
[final acceptance procedure](docs/guides/final-acceptance-testing.md).
The [documentation index](docs/index.md) links all published guides.
The [test-quality and mutation audit](docs/guides/test-mutation-audit.md) records
selected deliberate defects, missing assertions and the added regression scenarios.

## System contracts

Changes to financial, transport, or lifecycle requirements must update this
register and the linked detailed contract together. Keep proposals separate;
resolve conflicts with the assignment before adopting or implementing them.

| Contract | Required behavior and detail | Basis |
| --- | --- | --- |
| Durable usage and retries | Preserve increments under source-wide `(source, event_id)` identities across customers, sandboxes, processes, and restarts. Acknowledge committed input; unchanged retries preserve receipt time, changed content conflicts, and the sender retains unacknowledged input. [Event contract](docs/architecture/usage-to-invoice.md#3-usage-events-what-was-consumed). | Assignment delivery; system identity and inbox policies. |
| API and worker lifecycle | Separate processes and containers, independent restarts, shared PostgreSQL capacity. [Option C](docs/brainstorming/architecture-options.md#why-option-c-was-selected). | System architecture. |
| Historical pricing | Apply consumption-time overrides or defaults with finite, explicit starts. Preserve seed prices and dates; receipts fit one price version and UTC month. [Pricing](docs/architecture/accounting-rules.md#historical-prices-and-groups). | Assignment prices; system segmentation policy. |
| Generated resource IDs and command retries | Billing generates price IDs and derives readable new subscription IDs as `subscription/<customer_id>/<addon_name>` with each component percent-encoded. Prices, add-ons, credit grants, and spend-limit changes require JSON `idempotency_key`; durable keys and financial effects commit together. Missing or invalid keys return `422`; duplicate fields or wrong types return `400`; changed content conflicts. Existing IDs and financial history remain unchanged. [Command retries](docs/guides/api-reference.md#command-retries). | System API identity policy. |
| Accounting resource IDs | New rated groups use `grp_1`, `grp_2`, etc.; new credit ledger entries use `crd_1`, `crd_2`, etc. Each namespace has a persistent, noncycling PostgreSQL sequence starting at one. Allocation is safe across API and worker instances without a collision lookup. Rollback can leave gaps; existing IDs, references and invoices are preserved. [Identity rules](docs/architecture/accounting-rules.md#accounting-resource-identities). | System storage identity policy. |
| Price insertion | Ordinary new versions start at or after the server instant sampled under the catalog lock after identity checking. Explicit `effective_from: null` selects that instant in UTC at microsecond precision; the response and retries retain the stored timestamp. Earlier explicit activation returns `422`; identical retries succeed after activation. [Insertion contract](docs/architecture/accounting-rules.md#price-insertion-contract). | System activation policy. |
| Catalog recovery | Provision dated prices before metering. Missing prices quarantine without financial effects and report P0; repair through controlled SQL, then explicitly release. [Recovery contract](docs/architecture/accounting-rules.md#catalog-provisioning-and-p0-recovery). | System provisioning policy. |
| Exact money and credit | Allocate exact ticks before rounding; credit pays usage only and later grants do not rewrite allocations. [Money and credit](docs/architecture/accounting-rules.md#exact-money-and-credit). | Assignment credit; system precision/order. |
| Invoice presentation | Round cumulative groups half-up; displayed credit is rounded gross minus net. Preserve exact audit values and assignment totals. [Presentation](docs/architecture/accounting-rules.md#rounding-and-invoice-presentation). | Assignment cents/examples; system rounding. |
| Fixed monthly periods | Whole UTC calendar months, one immutable invoice per customer/month after month end; retries retain number and snapshot. [Months](docs/architecture/accounting-rules.md#fixed-calendar-month-contract). | Assignment periods; system closing guarantees. |
| Spend limits and add-ons | Compare original-month gross usage before credit, excluding add-ons; account beyond the limit. Charge the full add-on price from its purchase month. [Rules](docs/architecture/accounting-rules.md#utc-months-limits-add-ons-and-late-usage). | Assignment rules. |
| Late usage | Preserve consumption-month prices, limits, and audit; route closed/skipped-month usage to an eligible month after the latest closure and at or after receipt. Issued invoices never reopen. [Late usage](docs/architecture/accounting-rules.md#utc-months-limits-add-ons-and-late-usage). | Assignment example; system routing policy. |
| Atomic accounting and closing | Serialize customer financial writes; commit each receipt with its financial effects. Close already processed groups under the account lock without draining or blocking on pending/errors. First closing cannot skip earlier usage; later new invoices require the previous month closed. Publish closure, frozen groups, snapshot, and number atomically. [Closing contract](docs/architecture/accounting-rules.md#transaction-and-closing-contract). | System transaction policy. |
| Supported timestamps | After UTC conversion, years 1000 through 9999 with at most microsecond precision. [Event validation](docs/architecture/usage-to-invoice.md#3-usage-events-what-was-consumed). | System parsing boundaries. |

Automatic invoice scheduling/delivery, external invoice references, configurable
price denominators, and alternative catalog locking remain proposals.
