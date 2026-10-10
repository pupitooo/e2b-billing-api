# HTTP API reference

Run `make docs` and open the printed **API reference** address, by default
[http://127.0.0.1:8082/reference/](http://127.0.0.1:8082/reference/).
Scalar provides the three-panel `modern` layout and an embedded **Test Request**
client. The [OpenAPI 3.1.2 specification](../api/openapi.yaml) defines every endpoint's
fields, examples, validation, status codes, and retry/conflict semantics.

The `api-docs` Compose service serves Scalar and bundled JavaScript locally.
The `docs` service exposes both the guides and `/reference/` on one browser port.
Browser API requests go through the same-origin `/api` proxy to the API; no CORS
configuration is needed. `make api-docs` starts the same portal and prints its
reference link. Starting either target also starts PostgreSQL and the API, but
does not apply migrations.

Usage acceptance is asynchronous: `202` confirms the whole batch's durable
receipt, then the independent worker performs accounting. Keep `(source,
event_id)` and all event content unchanged on retries; changed content returns
`409`. A lost response or `503` can follow a successful commit, so retain the
input and retry with backoff. `GET /healthz` checks HTTP availability, not database
readiness. See [interfaces and ownership](../architecture/submission-overview.md#interfaces-and-ownership)
and the [event contract](../architecture/usage-to-invoice.md#3-usage-events-what-was-consumed).

API interface changes must update the specification in the same change. Refresh
Scalar after editing `openapi.yaml`; guides rebuild and refresh automatically.
See [documentation maintenance](documentation.md) for publication,
validation, and the one-port routing configuration.
