# Development conventions and static checks

## Project layout

- `cmd/billing-api/`: application entry point and server setup.
- `cmd/billing-worker/`: standalone worker startup, configuration, and signal handling.
- `cmd/platform-simulator/`: separate platform CLI entry point.
- `internal/`: private application packages, with `*_test.go` package tests next to the code.
- `migrations/`: numbered SQL migrations and the explicit PostgreSQL migration runner.
- `tests/api/`: HTTP integration tests against a running API, enabled with the `integration` build tag.
- `tests/inbox/`: PostgreSQL repository tests in isolated temporary schemas, enabled with the `integration` build tag.
- `tests/worker/`: executable lifecycle and exec health-check tests, enabled with the `integration` build tag.
- `tests/sql/`: database integrity tests executed with `psql`.
- `docs/`: project and interface documentation.

## Go formatting and static checks

Enable the tracked [pre-commit hook](../../.githooks/pre-commit) once after cloning:

```sh
make install-hooks
```

Every subsequent normal commit checks the staged snapshot with `gofmt`, the
`wsl` whitespace rules, and `go vet`, including integration-tagged test
code. A formatting violation or vet finding stops the commit. The hook preserves
partial staging and never formats or stages files automatically. Fix reported problems, then stage the intended
changes and commit again.

| Command | Behavior |
| --- | --- |
| `make fmt` | Apply `gofmt` plus `wsl` whitespace fixes. |
| `make fmt-check` | Check `gofmt` and `wsl`; change no files. |
| `make vet` | Run `go vet` for `cmd/`, `internal/`, and `tests/` with default and integration build tags. |
| `make check` | Run `gofmt`, `wsl`, and vet checks together, as CI does. |
| `make install-hooks` | Set this clone's `core.hooksPath` to the tracked `.githooks` directory. |

These commands use installed Go when available, otherwise Docker builds the
`go-tools` stage from the existing [Dockerfile](../../Dockerfile). No running API or
database is needed. `E2B_GO_CHECKS_DOCKER=1 make check` explicitly uses Docker;
CI uses this mode to match the pinned project toolchain. Local private `tools/`
worktrees and dependency directories are excluded from formatting.

The pinned `wsl` and Go analysis dependencies are defined in an isolated
[tool module](../../scripts/whitespace/go.mod); enabled checks are defined in
[scripts/whitespace.env](../../scripts/whitespace.env). Local commands and Docker use
the same versions and policy. They separate completed blocks and returns in longer blocks, keep error
checks next to their operations, and remove blank lines at block boundaries.
Declarations and their initialization may stay together. Both ordinary and
integration-tagged packages are checked. `make fmt` repairs these rules;
`make fmt-check`, the staged hook, and CI enforce them without editing files.
The first use with installed Go downloads the pinned tool and its dependencies.
Semantic grouping of preparation, calculation, persistence, and assertions still
requires review. See the [wsl documentation](https://github.com/bombsimon/wsl).

Use named constants for values that encode domain rules, supported schema or
checkpoint versions, protocol limits, retry policy, operational defaults, and
parsing boundaries. Name each constant for its meaning and keep it near the
owning responsibility; equal values with different meanings need distinct names.
Prefer standard-library constants where available.

Ordinary zero values, indexing, and counters may remain literal. Keep test inputs,
assignment fixtures, and expected outputs explicit; test expectations must not
derive from production constants. Review this semantic rule manually:
`make fmt` and `make check` do not enforce constant names.

Use underscore groups of three for decimal Go literals with five or more digits,
for example `10_000` and `1_000_000`. Fractional digits group from the decimal point, as in
`0.000_000_01`. Smaller literals may remain plain; existing separators must use
the same grouping. Calendar years stay plain, including `2026`, year-named values,
the year argument to `time.Date`, and comparisons with `Year()`. Markdown prose and
calculation examples follow the same number style; years, dates, identifiers,
URLs, and copyable language examples
retain their required syntax. AI-assisted edits and manual review maintain this
number style; `make fmt`, the staged hook, and CI do not repair or check it.

Hook configuration is local Git metadata and must be enabled in each clone.
Git permits bypassing local hooks with `--no-verify`; CI independently runs the
same checks on every push and pull request. The standard tools are documented
in [gofmt](https://pkg.go.dev/cmd/gofmt) and [go vet](https://pkg.go.dev/cmd/vet).
