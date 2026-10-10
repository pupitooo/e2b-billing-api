# Workload assumptions and local measurements

The assignment asks the design to consider approximately 50_000 customers,
each running from a few to thousands of sandboxes, with minute-based reports.
It does not prescribe an average sandbox count, batching topology, peak rate,
or latency target, and does not require the implementation to demonstrate that
capacity. For the following calculations, assume one metric and one event per
active sandbox per minute, coalesced by platform collectors across customers:

| Average active sandboxes per customer | Events/s | Requests/s at 1_000 events/batch |
| --- | --- | --- |
| 3 | 2_500 | 2.5 |
| 10 | 8_333.3 | 8.3 |
| 100 | 83_333.3 | 83.3 |

Rate = customers × average active sandboxes × metrics / 60. More metrics and
retries multiply this rate. A separate batch from every customer each minute
would instead mean about 833 HTTP requests/s, even with only ten events each.
The 1_000-event maximum also remains subject to the 1 MiB body limit. Spread
minute reports with jitter; a synchronized burst and recovery backlog need
separate capacity measurements and producer buffering.

On 2026-10-09, a local Docker/Linux arm64 sample used 16 concurrent writers, private
schemas, two runs of 50 new batches, PostgreSQL 18.6, and the existing synchronous
commit path. For 1_000-event batches, p95 latency including pool waiting was
306–324 ms with 4 connections, 260–409 ms with 8, and 209–217 ms with 16.
Short samples vary; more connections do not guarantee lower latency.
Eight connections are the initial per-process budget: these observed latencies
leave room under the ten-second deadline while reserving connections for
other processes. It is not a sustained throughput or production-capacity claim;
it excludes HTTP parsing, duplicate comparisons, accounting, growing indexes,
minute bursts, and failover. Increasing the pool alone does not establish support
for billions of monthly records.

Reproduce the short storage sample after starting PostgreSQL with
`make up SERVICE=postgres` (all benchmark data lives in owned temporary schemas):

```sh
docker compose build api
docker compose run --rm --no-deps api go test -tags=integration -run '^$' \
  -bench '^BenchmarkPostgresInsertBatch$' -benchtime=50x -cpu=16 -count=2 ./tests/inbox
```

Tune deployments from measured p95/p99 acceptance latency, connection-acquisition
waits, CPU, disk/WAL behavior, overload responses, and accounting backlog. At
8_333 events/s with 1_000-event batches, a measured mean transaction time of
0.2 s would imply about 1.7 occupied connections on average; p95 is not the mean
and this example does not cover peaks. Validate representative payloads, retries,
worker competition, and sustained storage growth before changing budgets or
adding replicas. See the [scaling proposal](../../docs/brainstorming/architecture-options.md#scaling-to-billions-of-records).
