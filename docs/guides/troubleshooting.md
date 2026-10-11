# Troubleshooting

## Troubleshoot simulator connectivity

For `lookup api on 127.0.0.11:53: no such host`, check which project the simulator
uses and whether that project's API is running. An API in another Compose
project cannot satisfy the internal `api` name. Inspect the projects and the
selected project's services:

```sh
docker compose ls
make ps
```

Restore the intended project and port exports from
[Compose projects and ports](local-development.md#compose-projects-and-ports), then start its
PostgreSQL with `make up SERVICE=postgres`, apply `make migrate`, and run
`make up`. Confirm API and worker health with `make ps` and use `make links` to
open that project's GUI and Adminer. Exporting host ports alone does not fix a
missing API in the simulator's network.

A simulator already retrying connection failures can continue when its API
becomes available. If you stop it with Ctrl+C, resume with the same project,
scenario, `SOURCE`, and `STATE`; do not start a second producer against the same
checkpoint. After it stops, `ACTION=status` reads its saved progress without
sending requests.

If Docker reports `port is already allocated`, inspect
`docker ps --format '{{.Names}}\t{{.Ports}}'`, select a free value for the affected
port export, and rerun `make up` in the same project. This changes host access
without selecting a different database or changing the simulator's internal URL.
Pending usage after HTTP `202` also requires a running worker; inspect it with
`make logs SERVICE=worker`.
