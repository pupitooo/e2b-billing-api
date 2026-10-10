# Documentation preview and maintenance

## Read the guides and API reference

```sh
make docs
make api-docs
make links
```

The `docs` service is the documentation portal, published on
`127.0.0.1:${E2B_DOCS_PORT:-8082}`. It serves Markdown guides with Material for
MkDocs navigation and browser-side search, and proxies `/reference/` to the
internal `api-docs` service. Scalar keeps the three-panel `modern` layout and
embedded request client. Its bundled JavaScript, configuration and specification
stay under `/reference/`; browser API calls use the same-origin `/api` proxy.
The reference groups endpoints by URL prefix, with category names `Customers`,
`Prices`, `Usage`, and `Healthz`. Scalar uses endpoint paths in sidebar navigation and search;
operation summaries describe their purpose in the endpoint details.
Scalar's introduction embeds the [API endpoint diagram](api-reference.md#api-endpoints-and-callers).
The `api-docs` service mounts only that diagram's directory and serves its PNG
and Mermaid source through explicit routes under `/reference/diagrams/api-endpoints/`.

Both Make targets start the same portal and its API/PostgreSQL dependencies.
They do not migrate the database or start the worker. Guides are still readable
while the API is stopped after initial startup; Scalar requests need a working
API. Use [local development](local-development.md) to initialize the database
and [runtime configuration](runtime-configuration.md) to diagnose startup.

The portal binds documentation read-only. Edits to selected Markdown, assets,
and navigation rebuild the site and refresh the browser automatically. Scalar
needs a browser refresh after OpenAPI changes. Restart `docs` after changing its
runtime scripts, publication hooks, or Caddy routing; rebuild it when the pinned images change.
Restart `api-docs` after changing Scalar's Caddy routing. Run `make docs` after
changing its Compose mounts or `API_REFERENCE_CONFIG` so that the container is
recreated with the new mounts and reference settings.

## Upgrade from the former Scalar-only docs service

Previously `docs` meant Scalar alone at `/`. It now means the full portal;
Scalar's service is named `api-docs` and its browser route is `/reference/`.
The existing `E2B_DOCS_PORT` still selects the one public documentation port.
No additional host port is exposed for `api-docs`.

Apply the rename and rebuild in place:

```sh
make docs
make links
```

Compose replaces the old `docs` container and starts `api-docs`; PostgreSQL and
simulator volumes remain in place. Update saved Scalar browser bookmarks to
`/reference/`, direct specification links to `/reference/openapi.yaml`, and
service-specific commands such as `make logs SERVICE=docs` to
`make logs SERVICE=api-docs` when investigating Scalar. `make logs SERVICE=docs`
now inspects MkDocs and the documentation gateway.

## Publication boundary

[Portal configuration](../site/mkdocs.yml) declares navigation pages and an
explicit asset list. [Publication hooks](../site/hooks.py) allow only those files
and generated theme assets into the build. A navigation entry alone does not
publish every Markdown file in the source directory. Unknown local documents,
JSON plans, and Mermaid sources are excluded unless explicitly listed.

Keep every public guide in English. Use ordinary relative Markdown links within
`docs/`; they work both in repository viewers and the generated site. Links to
repository files outside `docs/` are rewritten to the corresponding GitHub file
only while rendering, so the portal does not expose source code or local files.
OpenAPI source links resolve to `/reference/openapi.yaml` in the portal. Keep
guide routes outside `/api/` and `/reference/`, which are reserved for proxies;
the build rejects colliding navigation pages. The root README remains the
operational entry point and contract register.

For a new guide:

1. Choose its subfolder by purpose and add its explicit `.gitignore` exception.
2. Add it to navigation in `docs/site/mkdocs.yml` and to [the public index](../index.md).
3. Add an entry in the local documentation overview; link important guides from the root README.
4. List any new downloadable assets explicitly. Keep each diagram's English
   Mermaid source and PNG together; update the public diagram index and the local canonical diagram overview.
5. Run the documentation check and inspect the affected page in the browser.

The local `docs/README.md`, full diagram catalog, original assignment, business
notes, and brainstorming working documents remain ignored. Do not publish them
or use force-add to bypass the selected scope.

## Verify documentation

```sh
make docs-check
```

This builds the same pinned portal image and runs `mkdocs build --strict` in a
one-off container without starting the database, API or worker. Missing selected
files, unresolved document links, and invalid anchors fail the build. Generated
HTML is temporary container data; nothing is written into the checkout.
Isolated build scenarios also verify that private Markdown and JSON stay out of
the output and search index, theme assets survive, and missing selected documents
broken links/anchors, or navigation under reserved proxy routes fail. CI runs the same checks before the application suite.

The documentation image uses pinned Material and Scalar images. The Material
image supplies MkDocs, its extensions and theme; Caddy is copied from the same
Scalar image used by `api-docs`. The runtime supervises the preview and gateway
processes together: either exiting stops the container, and termination stops
both. Compose readiness checks both the guides and proxied Scalar reference.
