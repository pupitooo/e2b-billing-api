#!/bin/sh
set -eu

mode=${1:-check}
case "$mode" in
  fmt|fmt-check|vet|check) ;;
  *) printf 'Unknown Go quality command: %s\n' "$mode" >&2; exit 2 ;;
esac

# CI uses Docker's pinned toolchain; local commands can also use installed Go.
# The tools stage downloads dependencies without building or starting the API.
if [ "${E2B_GO_CHECKS_DOCKER:-0}" = 1 ] || ! command -v go >/dev/null 2>&1; then
  image=$(docker build --quiet --target go-tools .)
  exec docker run --rm --user "$(id -u):$(id -g)" \
    -e GOCACHE=/tmp/go-build -e GOTOOLCHAIN=local \
    --volume "$PWD:/workspace" --workdir /workspace "$image" \
    sh scripts/go-quality.sh "$mode"
fi

formatter="$(go env GOROOT)/bin/gofmt"
. ./scripts/whitespace.env

# The Docker image contains the pinned binary; installed Go uses the same release.
# Both passes include integration-only tests without executing them.
whitespace_go() {
  for build_tags in '' integration; do
    if [ -x /usr/local/bin/e2b-wsl ]; then
      /usr/local/bin/e2b-wsl -default none -enable "$WSL_CHECKS" \
        -branch-max-lines "$WSL_BRANCH_MAX_LINES" -tags "$build_tags" "$@" ./cmd/... ./internal/... ./tests/...
    else
      go run -mod=readonly -modfile=./scripts/whitespace/go.mod github.com/bombsimon/wsl/v5/cmd/wsl \
        -default none -enable "$WSL_CHECKS" \
        -branch-max-lines "$WSL_BRANCH_MAX_LINES" -tags "$build_tags" "$@" ./cmd/... ./internal/... ./tests/...
    fi
  done
}

# Check or repair first-party Go files without traversing private worktrees,
# Git metadata, or third-party dependency trees. find preserves spaced paths.
format_go() {
  find . -type d \( -name .git -o -name tools -o -name vendor -o -name node_modules \) -prune \
    -o -type f -name '*.go' -exec "$formatter" "$1" {} +
}

# A formatter mismatch must fail checks even though gofmt -l itself exits zero.
# Checks report files and leave the working tree and staging area untouched.
check_format() {
  unformatted=$(format_go -l)
  if [ -n "$unformatted" ]; then
    printf 'Go formatting differs from gofmt:\n%s\nRun make fmt and stage the intended changes again.\n' "$unformatted" >&2
    return 1
  fi
}

# Vet checks first-party packages and integration-only tests without executing
# them. Match the linter's package scope so ignored local tooling stays excluded.
vet_go() {
  go vet ./cmd/... ./internal/... ./tests/...
  go vet -tags=integration ./cmd/... ./internal/... ./tests/...
}

case "$mode" in
  fmt) format_go -w; whitespace_go -fix; format_go -w ;;
  fmt-check) check_format; whitespace_go ;;
  vet) vet_go ;;
  check) check_format; whitespace_go; vet_go ;;
esac
