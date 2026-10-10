#!/bin/sh
set -eu

# Supervise preview and gateway together; an exited child fails the container.
mkdocs serve --strict --config-file /source/site/mkdocs.yml --dev-addr 127.0.0.1:8000 &
preview_pid=$!
caddy run --config /source/site/Caddyfile --adapter caddyfile &
gateway_pid=$!

cleanup() {
    kill "$preview_pid" "$gateway_pid" 2>/dev/null || true
    wait "$preview_pid" "$gateway_pid" 2>/dev/null || true
}

trap cleanup EXIT
trap 'exit 0' INT TERM

# BusyBox ash supports waiting for either child; preserve a nonzero exit.
set +e
wait -n "$preview_pid" "$gateway_pid"
child_status=$?
set -e
if [ "$child_status" -eq 0 ]; then
    exit 1
fi
exit "$child_status"
