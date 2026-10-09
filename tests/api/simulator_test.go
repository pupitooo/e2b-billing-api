//go:build integration

package api_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSimulatorExecutableLostResponseAndAssignment launches the separate Go
// executable against the running HTTP API. Lost acknowledgement and regrouped
// restart must preserve inbox rows; explicit advancement then delivers the six
// assignment measurements with their original consumption times and units.
func TestSimulatorExecutableLostResponseAndAssignment(t *testing.T) {
	pool, source := apiFixture(t)
	binary := simulatorExecutable(t)
	state := filepath.Join(t.TempDir(), "run.json")
	base := []string{"--state=" + state, "--api-url=" + apiURL(), "--source=" + source, "--retry-min=1ms", "--retry-max=2ms"}
	output := simulatorCommand(t, binary, true, append(base, "--scenario=lost-response", "--mode=step", "--max-attempts=1")...)
	if !strings.Contains(output, "generated=2 pending=2 delivered=0") || apiEventCount(t, pool, source) != 2 {
		t.Fatalf("Lost acknowledgement was not backed by durable receipt: %s", output)
	}
	before := apiSnapshot(t, pool, source)
	simulatorCommand(t, binary, false, append(base, "--action=send", "--batch-size=1")...)
	if after := apiSnapshot(t, pool, source); after != before {
		t.Error("Restarted sender changed committed measurements or receipts")
	}
	simulatorCommand(t, binary, false, append(base, "--mode=fast")...)
	if count := apiEventCount(t, pool, source); count != 4 {
		t.Fatalf("October phase committed %d events, want 4", count)
	}
	output = simulatorCommand(t, binary, false, append(base, "--mode=fast")...)
	if !strings.Contains(output, "Waiting at barrier") || apiEventCount(t, pool, source) != 4 {
		t.Fatal("Late October was released before explicit advancement")
	}
	simulatorCommand(t, binary, false, append(base, "--advance=true", "--mode=fast")...)
	rows, err := pool.Query(context.Background(), `
		SELECT customer_id, period_start, period_end, units, metric
		FROM usage_inbox WHERE source = $1 ORDER BY period_start, customer_id`, source)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := []struct {
		customer string
		start    string
		units    int64
	}{
		{"acme", "2026-10-10T12:00:00Z", 100_000_000},
		{"cyberdyne", "2026-10-10T12:00:00Z", 123_456_789},
		{"acme", "2026-10-20T12:00:00Z", 200_000_000},
		{"cyberdyne", "2026-10-20T12:00:00Z", 200_000_000},
		{"acme", "2026-10-30T12:00:00Z", 50_000_000},
		{"acme", "2026-11-03T12:00:00Z", 100_000_000},
	}
	index := 0
	for rows.Next() {
		var customer, metric string
		var start, end time.Time
		var units int64
		if err := rows.Scan(&customer, &start, &end, &units, &metric); err != nil {
			t.Fatal(err)
		}
		if index >= len(want) {
			t.Fatal("Simulator inserted extra assignment events")
		}
		expected := want[index]
		if customer != expected.customer || start.UTC().Format(time.RFC3339) != expected.start ||
			end.Sub(start) != time.Hour || units != expected.units || metric != "cpu_seconds" {
			t.Errorf("Committed fixture %d = %s %s %s %d %s", index, customer, start, end, units, metric)
		}
		index++
	}
	if err := rows.Err(); err != nil || index != len(want) {
		t.Fatalf("Assignment rows = %d, %v", index, err)
	}
	rows.Close()
	before = apiSnapshot(t, pool, source)
	simulatorCommand(t, binary, false, append(base, "--action=replay", "--scenario=duplicates", "--batch-size=1", "--reverse=true")...)
	if after := apiSnapshot(t, pool, source); after != before {
		t.Error("Repeated executable replay changed the stored inbox")
	}
}

// TestSimulatorExecutableOfflineBuffer creates measurements without billing,
// fails delivery to an unavailable endpoint, then resumes in a new process
// against the real API. The generated step must survive and commit exactly once.
func TestSimulatorExecutableOfflineBuffer(t *testing.T) {
	pool, source := apiFixture(t)
	binary := simulatorExecutable(t)
	state := filepath.Join(t.TempDir(), "run.json")
	base := []string{"--state=" + state, "--source=" + source, "--mode=step"}
	simulatorCommand(t, binary, false, append(base, "--action=generate")...)
	if count := apiEventCount(t, pool, source); count != 0 {
		t.Fatalf("Generation contacted billing: %d rows", count)
	}
	output := simulatorCommand(t, binary, true, append(base, "--action=send", "--api-url=http://127.0.0.1:1", "--max-attempts=1")...)
	if !strings.Contains(output, "pending=2 delivered=0") || apiEventCount(t, pool, source) != 0 {
		t.Fatalf("Offline delivery lost pending buffer: %s", output)
	}
	simulatorCommand(t, binary, false, append(base, "--action=send", "--api-url="+apiURL())...)
	if count := apiEventCount(t, pool, source); count != 2 {
		t.Fatalf("Recovered offline measurements = %d, want 2", count)
	}
	simulatorCommand(t, binary, false, append(base, "--action=send", "--api-url="+apiURL())...)
	if count := apiEventCount(t, pool, source); count != 2 {
		t.Error("Already confirmed buffer inserted additional rows")
	}
}

// simulatorExecutable selects the container-built CLI, an explicit local
// binary, or a local build so integration tests retain their documented workflow.
func simulatorExecutable(t *testing.T) string {
	t.Helper()
	if binary := os.Getenv("E2B_SIMULATOR_BINARY"); binary != "" {
		return binary
	}
	if binary, err := exec.LookPath("platform-simulator"); err == nil {
		return binary
	}
	binary := filepath.Join(t.TempDir(), "platform-simulator")
	command := exec.Command("go", "build", "-o", binary, "../../cmd/platform-simulator")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Build separate simulator executable: %v\n%s", err, output)
	}
	return binary
}

// simulatorCommand runs an independent producer process and checks its exit
// outcome. A bounded context prevents a regression from leaving CI retrying.
func simulatorCommand(t *testing.T, binary string, wantFailure bool, arguments ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, arguments...).CombinedOutput()
	if ctx.Err() != nil || (err != nil) != wantFailure {
		t.Fatalf("Simulator exit = %v (want failure %v), context %v\n%s", err, wantFailure, ctx.Err(), output)
	}
	return string(output)
}
