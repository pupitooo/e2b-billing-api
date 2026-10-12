#!/usr/bin/env bash
set -euo pipefail

# One fresh project owns the runner, database, network, and volumes. Never use
# the caller's development project or credentials, including values in .env.
export E2B_E2E_ROOT="$PWD"
mkdir -p tools/e2e-results
export E2B_E2E_OUTPUT
E2B_E2E_OUTPUT=$(mktemp -d "$PWD/tools/e2e-results/e2b-e2e-XXXXXX")
export E2B_E2E_PROJECT
E2B_E2E_PROJECT=$(basename "$E2B_E2E_OUTPUT" | tr '[:upper:]' '[:lower:]')
export E2B_POSTGRES_PORT=0 E2B_API_PORT=0 E2B_POSTGRES_PASSWORD=e2b_local_dev
export E2B_DEMO_NOW=

docker_endpoint=${DOCKER_HOST:-$(docker context inspect --format '{{.Endpoints.docker.Host}}')}
case "$docker_endpoint" in
  unix://*) export E2B_E2E_SOCKET=${docker_endpoint#unix://} ;;
  *) printf '%s\n' 'E2E tests require a local Docker Unix socket.' >&2; exit 2 ;;
esac

compose=(docker compose --env-file /dev/null --project-name "$E2B_E2E_PROJECT" --file compose.yaml)

# Preserve evidence before removing only the project created by this invocation.
cleanup() {
  result=$?
  trap - EXIT
  "${compose[@]}" logs --no-color > "$E2B_E2E_OUTPUT/services.log" 2>&1 || true
  "${compose[@]}" ps --all > "$E2B_E2E_OUTPUT/services.txt" 2>&1 || true
  if ! "${compose[@]}" down --volumes --remove-orphans > "$E2B_E2E_OUTPUT/cleanup.log" 2>&1; then
    cat "$E2B_E2E_OUTPUT/cleanup.log" >&2
    result=1
  fi
  printf 'E2E evidence: %s\n' "$E2B_E2E_OUTPUT"
  exit "$result"
}
trap cleanup EXIT

printf 'E2E project: %s\n' "$E2B_E2E_PROJECT"
"${compose[@]}" build api worker e2e
"${compose[@]}" up -d --wait --wait-timeout 120 postgres
"${compose[@]}" run --rm --no-deps --entrypoint sh e2e -c \
  'go test -tags=integration -count=1 -v -timeout=20m -run "$1" ./tests/e2e; result=$?; chmod -R a+rX /evidence || exit 1; exit "$result"' \
  e2e-run "${RUN:-}" \
  2>&1 | tee "$E2B_E2E_OUTPUT/tests.log"
