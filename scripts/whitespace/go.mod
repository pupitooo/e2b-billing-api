// Isolated linter dependencies; x/tools v0.50.0 supports Go 1.27 export data.
// v0.51.0 removed required imports during wsl fixes in project verification.
module e2b/whitespace-tools

go 1.27.0

require (
	github.com/bombsimon/wsl/v5 v5.9.0
	golang.org/x/tools v0.50.0
)

require (
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
)
