//go:build integration

package e2e_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	composeTimeout      = 2 * time.Minute
	observationTimeout  = 15 * time.Second
	observationInterval = 20 * time.Millisecond
)

type environment struct {
	root, project, evidence, now string
	pool                         *pgxpool.Pool
	transition                   int
}

// newEnvironment admits only the generated isolated project, stops application
// processes, resets its owned database, and registers diagnostics before cleanup.
func newEnvironment(t *testing.T, name string) *environment {
	t.Helper()
	project := os.Getenv("E2B_E2E_PROJECT")
	root := os.Getenv("E2B_E2E_ROOT")
	evidence := os.Getenv("E2B_E2E_EVIDENCE")
	if !strings.HasPrefix(project, "e2b-e2e-") || root == "" || evidence == "" {
		t.Fatal("Run through make e2e-test; an isolated project, root and evidence directory are required")
	}

	e := &environment{root: root, project: project, evidence: filepath.Join(evidence, name)}
	if err := os.MkdirAll(e.evidence, 0o755); err != nil {
		t.Fatalf("Create scenario evidence %s: %v", name, err)
	}
	e.compose(t, "stop", "api", "worker")
	ctx, cancel := context.WithTimeout(context.Background(), observationTimeout)
	defer cancel()
	pool, err := pgxpool.New(ctx, "postgres://e2b:e2b_local_dev@postgres:5432/e2b_billing?sslmode=disable")
	if err != nil {
		t.Fatalf("Open isolated verification database: %v", err)
	}
	e.pool = pool
	t.Cleanup(func() {
		e.captureLogs(t)
		e.compose(t, "stop", "api", "worker")
		pool.Close()
	})
	if _, err := pool.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatalf("Reset owned database in project %s: %v", project, err)
	}
	return e
}

// command creates bounded Compose commands against the generated project only.
// Every application process receives the same fixed business clock and keeps
// real runtime deadlines; the test uses a short ordinary worker poll interval.
func (e *environment) command(ctx context.Context, arguments ...string) *exec.Cmd {
	base := []string{"compose", "--env-file", "/dev/null", "--project-name", e.project, "--file", filepath.Join(e.root, "compose.yaml")}
	command := exec.CommandContext(ctx, "docker", append(base, arguments...)...)
	command.Dir = e.root
	command.Env = append(os.Environ(), "E2B_DEMO_NOW="+e.now, "E2B_WORKER_POLL_INTERVAL=10ms")
	return command
}

// compose records the command outcome and fails on execution errors before any
// scenario assertion, keeping infrastructure failures distinct from HTTP faults.
func (e *environment) compose(t *testing.T, arguments ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), composeTimeout)
	defer cancel()
	output, err := e.command(ctx, arguments...).CombinedOutput()
	e.save(t, "compose.log", fmt.Sprintf("%v\n%s\n", arguments, output), true)
	if err != nil {
		t.Fatalf("Compose %v in %s at %s: %v\n%s", arguments, e.project, e.now, err, output)
	}
	return strings.TrimSpace(string(output))
}

// migrate uses the same transaction and advisory-lock entry point as make
// migrate, rather than replaying individual migration files in the Go test.
func (e *environment) migrate(t *testing.T) {
	t.Helper()
	e.compose(t, "exec", "-T", "postgres", "psql", "-X", "-U", "e2b", "-d", "e2b_billing", "-v", "ON_ERROR_STOP=1", "-f", "/migrations/migrate.sql")
}

// start fixes both application clocks through normal environment configuration
// and waits for their actual health probes without starting browser services.
func (e *environment) start(t *testing.T, now string) {
	t.Helper()
	e.now = now
	e.compose(t, "up", "-d", "--no-deps", "--wait", "--wait-timeout", "90", "api", "worker")
}

// advance changes time only after ordinary pending receipts have completed.
// Graceful stop drains operations before recreating both containers; PostgreSQL
// and producer files survive and runtime clocks are never changed.
func (e *environment) advance(t *testing.T, now string) {
	t.Helper()
	if now == e.now {
		return
	}
	e.awaitSQL(t, "clock transition drains pending receipts", `SELECT count(*) FROM usage_inbox WHERE processed_at IS NULL AND processing_error IS NULL`, "0")
	e.captureLogs(t)
	e.compose(t, "stop", "api", "worker")
	e.start(t, now)
}

// captureLogs retains each container generation before a clock change removes
// it, including failure diagnostics even when the outer script later cleans up.
func (e *environment) captureLogs(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), composeTimeout)
	defer cancel()
	output, err := e.command(ctx, "logs", "--no-color", "api", "worker", "postgres").CombinedOutput()
	e.transition++
	e.save(t, fmt.Sprintf("services-%03d.log", e.transition), string(output), false)
	if err != nil {
		t.Errorf("Capture service logs: %v\n%s", err, output)
	}
}

// save retains scenario inputs and observed results outside application volumes
// so normal teardown never removes the evidence used to diagnose a CI failure.
func (e *environment) save(t *testing.T, name, value string, appendFile bool) {
	t.Helper()
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendFile {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	file, err := os.OpenFile(filepath.Join(e.evidence, name), flags, 0o644)
	if err != nil {
		t.Fatalf("Open evidence %s: %v", name, err)
	}
	if _, err := file.WriteString(value); err != nil {
		file.Close()
		t.Fatalf("Write evidence %s: %v", name, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close evidence %s: %v", name, err)
	}
}
