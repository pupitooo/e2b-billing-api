# Final acceptance test procedure

## Contents

Read the [introduction](#introduction), then start with section 1.

| Section | Action and inputs | Expected result |
| --- | --- | --- |
| [1. Preparation](acceptance/preparation.md#1-initial-setup-and-test-preparation) | Check tools, prepare evidence, review seed inputs, calendar prerequisites, and reset procedure. | Required tools work; checklist starts at **NOT RUN**; scenario preconditions are understood. |
| [2. Automated gate](acceptance/automated-checks.md#2-automated-regression-gate-in-an-isolated-project) | Run migrations, static checks, the complete suite, and concurrent migration runners in an isolated project. | All checks pass, including SQL integrity in UTC and Asia/Shanghai; evidence survives reset. |
| [3. Seed inspection](acceptance/assignment.md#3-fresh-assignment-environment-and-seed-inspection) | Check services, server clock, served specification, and seed data. | Healthy services, matching OpenAPI, literal seed catalog, and empty accounts. |
| [4. Assignment and transport failures](acceptance/assignment.md#4-walk-through-the-complete-assignment-and-transport-failures) | Execute the 24-step assignment with API downtime, a lost grant reply, a stopped worker, and receipt replay. | Recovery completes every step once; durable receipts and unchanged retry metadata are verified. |
| [5. Invoices and database](acceptance/assignment.md#5-final-invoices-and-independently-persisted-state) | Compare full invoice responses and independent SQL results. | Literal amounts, balances, numbering, and record counts match. |
| [6. Additional checks](acceptance/final-checks.md#6-remaining-business-integrity-and-concurrency-scenarios) | Inspect named regression results, diagnose failures before the gate reset, and read the current-month API after section 5. | Required scenarios actually pass; the live route returns the server's UTC month. |
| [7. Restart](acceptance/final-checks.md#7-persistence-and-immutable-reads-across-restart) | Restart services, reread invoices and state, then save evidence and reset. | Invoices, balances, database state, and producer progress remain unchanged across restart. |
| [8. Credit and spend limit](acceptance/final-checks.md#8-credit-covered-gross-spend-and-add-on-exclusion) | Grant credit, buy an add-on, exceed a gross usage limit, issue an invoice, and raise the limit. | Credit-covered usage reaches the limit; add-ons are excluded; all usage remains billed. |
| [9. Completion](acceptance/final-checks.md#9-architecture-review-limitations-and-completion-record) | Review responsibilities and limits; complete the evidence checklist. | Each requirement has **PASS**, **FAIL**, or **NOT RUN**, with evidence and any limitation. |

## Introduction

Use this procedure to review one revision of the complete billing service. Run the
automated regression gate, walk through the assignment using the public API, and
compare its responses with persisted accounting state. Record evidence before
declaring a requirement passed. A successful HTTP request alone is insufficient.

Use the [API contract](/reference/),
[financial rules](../architecture/accounting-rules.md), and
[named public workflows](../simulator/billing-scenarios.md) for implementation
details. The original assignment is supplied separately.

Each section's step table states the action, inputs, and expected result before
the executable commands. Detailed financial tables provide literal values for
comparison. Follow the linked steps in order; optional **Complete commands**
blocks combine the same commands for convenience.

### Conventions used in this guide

- All monetary expectations use the assignment's USD seed catalog.
- Calendar months are UTC.
- One cent is 1_000_000 microcents.
- Invoice amounts are integer cents; exact API amounts are decimal strings.
- Underscores in prose and tables group digits; copyable JSON and SQL retain
  their valid numeric syntax.

## Reading order

These pages form one sequential procedure. Keep the same shell session and
carry its variables, helper functions, project selection, and evidence directory
forward. A new page does not start an independent scenario. Execute resets only
at the existing checkpoints 2.5, 7.4, and 8.4, after preserving evidence.

1. [Preparation](acceptance/preparation.md)
2. [Automated checks](acceptance/automated-checks.md)
3. [Assignment walkthrough](acceptance/assignment.md)
4. [Final checks](acceptance/final-checks.md)
