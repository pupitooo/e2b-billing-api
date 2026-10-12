# Run backend scenarios manually

This guide is for inspecting each intermediate billing result. The
[E2E scenarios](e2e-testing.md) explain what must happen and which automated tests
verify it. Running a CLI fixture against an existing server is a manual presentation;
`make e2e-test` creates, drives, verifies and cleans up its own environment.

## Prepare one isolated presentation environment

Use Bash or Zsh in the repository root. Choose an unused project name and free
ports. The following starts a fresh manual environment without touching `e2b-local`:

```sh
export COMPOSE_PROJECT_NAME=e2b-manual-e2e
export E2B_API_PORT=28081 E2B_POSTGRES_PORT=25433 E2B_DOCS_PORT=28082
export E2B_DEMO_NOW=2026-10-31T00:00:00Z
make up SERVICE=postgres
make migrate
make up SERVICE=api
make up SERVICE=worker
manual_api=http://127.0.0.1:$E2B_API_PORT
curl --fail-with-body "$manual_api/healthz"
```

Stop at any command failure. Expected health is HTTP 200. Before the first
financial action, compare seed data with [acceptance section 3.4](acceptance/assignment.md#34-query-and-compare-the-seed-data).
If these accounts have already been used, save their evidence and reset only this
selected project using [the reset procedure](#reset-between-independent-scenarios).

Optional `make docs` starts Scalar and database browsing for visual inspection.
Backend checks below work entirely from the terminal.

## Change time without removing history

Define this helper once in the same shell. It refuses a clock jump while ordinary
receipts remain pending, stops both business processes and recreates them with
one shared setting. Inspect a failed pending check before continuing.

```sh
manual_advance() {
  (
    set -euo pipefail
    test "${COMPOSE_PROJECT_NAME:-}" = e2b-manual-e2e
    manual_pending=$(docker compose exec -T postgres psql -X -At -U e2b -d e2b_billing \
      -v ON_ERROR_STOP=1 -c 'SELECT count(*) FROM usage_inbox WHERE processed_at IS NULL AND processing_error IS NULL')
    test "$manual_pending" = 0 || {
      printf 'Wait for pending accounting before changing time: %s receipts\n' "$manual_pending" >&2
      exit 1
    }
    docker compose stop api worker
    E2B_DEMO_NOW="$1" make up SERVICE=api
    E2B_DEMO_NOW="$1" make up SERVICE=worker
  ) || return $?
  export E2B_DEMO_NOW="$1"
  docker compose exec -T api printenv E2B_DEMO_NOW
  docker compose exec -T worker printenv E2B_DEMO_NOW
}
```

Both printed timestamps must match the requested UTC instant. OS `date` continues
to show real time; it does not show the billing clock. `make restart` cannot apply
new environment variables. For optional portal presentation also recreate docs
with `make up SERVICE=docs` after exporting the new value.

## Execute the assignment one step at a time

Select the canonical fixture and a durable checkpoint:

```sh
export SOURCE=manual-assignment STATE=/state/manual-assignment.json
manual_next() {
  make simulate SCENARIO=billing-assignment MODE=step MAX_ATTEMPTS=10
}
manual_next
```

Each invocation sends the next named public operation and validates its declared
response. Compare its name and `completed=N steps=24` with the
[24-step table](../simulator/billing-scenarios.md#billing-assignment).
A failure retains the cursor; retry with the same source, state and original input.

| Phase | Before running | Steps to execute | Inspect before moving on |
| --- | --- | --- | --- |
| October accounting | Initial clock `2026-10-31T00:00:00Z` | 1–12, one `manual_next` per step | Acme credit USD 13; Cyberdyne October gross 1_817_283_945 microcents, reached USD 15 limit; no pending/errors |
| October publication and late receipt | `manual_advance 2026-11-01T00:00:00Z` | 13–18 | Acme October invoice USD 20; Cyberdyne October USD 18.17; late October receipt has November receipt time; Acme credit USD 7; November Cyberdyne gross zero |
| November publication and immutable reads | `manual_advance 2026-12-01T00:00:00Z` | 19–24 | Acme November USD 20 with late October line; original October snapshots unchanged; completed count 24 |

Inspect account and persisted receipt state at any checkpoint:

```sh
curl --fail-with-body "$manual_api/customers/acme/credit" | jq .
curl --fail-with-body "$manual_api/customers/cyberdyne/months/2026-10/limit-status" | jq .
docker compose exec -T postgres psql -X -U e2b -d e2b_billing -v ON_ERROR_STOP=1 \
  -c 'SELECT source,event_id,received_at,processed_at,processing_error FROM usage_inbox ORDER BY source,event_id'
make simulate SCENARIO=billing-assignment ACTION=status
```

For the operator faults, pause at the same checkpoints as
[acceptance section 4](acceptance/assignment.md#4-walk-through-the-complete-assignment-and-transport-failures):
stop API before step 1, force one attempt, restart and resume; force one attempt
at the lost grant reply in step 3; stop worker before step 6 and verify two pending
receipts with no ratings, then restart before step 7. Snapshot complete receipts
before and after step 9. The failed producer must retain its previous cursor.

Compare the complete three invoices and independent SQL with
[acceptance section 5](acceptance/assignment.md#5-final-invoices-and-independently-persisted-state),
then [restart and reread](acceptance/final-checks.md#7-persistence-and-immutable-reads-across-restart)
before resetting. Do not infer correct money merely from HTTP 200.

## Other public fixtures

Reset to fresh seed accounts between independent fixtures. Set its initial clock,
choose its name and a new state, then run one step per command:

```sh
make simulate SCENARIO=billing-exact-credit MODE=step \
  SOURCE=manual-exact-credit STATE=/state/manual-exact-credit.json
```

| Fixture | Initial business time | Changes before named steps |
| --- | --- | --- |
| `billing-demo-invoice` | `2026-11-01T00:00:00Z` | None |
| `billing-command-retries`, `billing-exact-credit`, `billing-credit-exhaustion`, `billing-limit-status`, `billing-price-boundary`, `billing-month-boundary` | `2026-12-01T00:00:00Z` | None; explicit month paths supply historical month reads |
| `billing-addon-order`, `billing-subcent-invoices` | `2027-01-01T00:00:00Z` | None |
| `billing-price-versions` | `2026-10-01T00:00:00Z` | Before step 4: `2026-11-01T00:00:00Z`; before step 6: `2026-12-01T00:00:00Z` |
| `billing-delayed-platform`, `billing-missed-invoice` | First step's `business_time` | Each new phase uses the fixture's declared `business_time` |

For timeline fixtures, display the inputs, expected response and next clock
before executing each operation:

```sh
jq '.steps[] | {name,business_time,request,want}' docs/simulator/billing-delayed-platform.json
```

The [scenario catalog](../simulator/billing-scenarios.md) and
[delayed billing guide](../simulator/delayed-billing.md) explain intermediate amounts.
The CLI's `business_time` metadata does not itself change the server; invoke
`manual_advance` before the next phase. Full-speed mode cannot cross clock phases
for you. A new source or state file does not reset an account.

For the combined credit/add-on/limit case, follow all twelve explicit requests and
SQL checks in [acceptance section 8](acceptance/final-checks.md#8-credit-covered-gross-spend-and-add-on-exclusion).
That scenario's invoice clock is `2026-11-01T00:00:00Z`.

## Six-month clock jump

After resetting to fresh accounts, set `manual_advance 2026-10-01T00:00:00Z`.
Buy the pack:

```sh
curl --fail-with-body -H 'Content-Type: application/json' \
  -d '{"idempotency_key":"half-year-pack","addon_name":"concurrency_pack","purchased_at":"2026-10-01"}' \
  "$manual_api/customers/acme/addons" | jq .
manual_advance 2027-03-31T23:59:59.999999Z
curl -sS -w '\nHTTP %{http_code}\n' -H 'Content-Type: application/json' \
  -d '{"idempotency_key":"six-months","through_month":"2027-03"}' \
  "$manual_api/customers/acme/invoices/catch-up"
```

Expect HTTP 422, `invalid_command`, field `through_month`, and zero invoices.
Then cross the boundary and resend exactly the same command:

```sh
manual_advance 2027-04-01T00:00:00Z
curl --fail-with-body -H 'Content-Type: application/json' \
  -d '{"idempotency_key":"six-months","through_month":"2027-03"}' \
  "$manual_api/customers/acme/invoices/catch-up" | jq .
docker compose exec -T postgres psql -X -U e2b -d e2b_billing -v ON_ERROR_STOP=1 \
  -c 'SELECT billing_month,invoice_number,total_cents FROM invoices ORDER BY billing_month'
```

Expect six October–March invoices, `ACME-0001` through `ACME-0006`, each 2_000
cents, total 12_000 cents; credit balance and ledger entries remain zero. Resend
purchase and catch-up with the original keys: all original snapshots, timestamps,
subscription count and numbering must remain unchanged. This is explicit recovery,
not automatic six-month scheduling.

## Reset between independent scenarios

Save CLI status, HTTP responses, SQL output and service logs outside Docker first.
Only then select the next fixture's initial timestamp and reset this presentation project.
The example timestamp suits the exact-credit fixture:

```sh
export E2B_DEMO_NOW=2026-12-01T00:00:00Z
(
  set -euo pipefail
  test "${COMPOSE_PROJECT_NAME:-}" = e2b-manual-e2e
  docker compose down --volumes
  make up SERVICE=postgres
  make migrate
  make up SERVICE=api
  make up SERVICE=worker
)
```

This deletes that project's database and simulator checkpoint. Set the next
scenario's initial `E2B_DEMO_NOW` before starting API/worker. Keep your evidence;
a reset cannot repair a failed scenario's financial expectations.
