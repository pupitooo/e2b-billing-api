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

// The simulator executable delivers the assignment through the live API and
// preserves committed rows while recovering from lost responses and offline delivery.
func TestSimulatorExecutable(t *testing.T) {
	t.Run("simulator executable lost response and assignment", func(t *testing.T) {
		steps := []struct {
			name                  string
			arguments             []string
			wantFailure           bool
			wantOutput            string
			wantRows              int
			wantSnapshotUnchanged bool
		}{
			{
				name:        "lose the first acknowledgement",
				arguments:   []string{"--scenario=lost-response", "--mode=step", "--max-attempts=1"},
				wantFailure: true,
				wantOutput:  "generated=2 pending=2 delivered=0",
				wantRows:    2,
			},
			{
				name:                  "resume with one-event batches",
				arguments:             []string{"--action=send", "--batch-size=1"},
				wantFailure:           false,
				wantRows:              2,
				wantSnapshotUnchanged: true,
			},
			{
				name:        "deliver October phase",
				arguments:   []string{"--mode=fast"},
				wantFailure: false,
				wantRows:    4,
			},
			{
				name:        "hold the operator barrier",
				arguments:   []string{"--mode=fast"},
				wantFailure: false,
				wantOutput:  "Waiting at barrier",
				wantRows:    4,
			},
			{
				name:        "advance past the barrier",
				arguments:   []string{"--advance=true", "--mode=fast"},
				wantFailure: false,
				wantRows:    6,
			},
		}
		wantAssignment := []struct {
			customer string
			start    string
			units    int64
		}{
			{
				customer: "acme",
				start:    "2026-10-10T12:00:00Z",
				units:    100_000_000,
			},
			{
				customer: "cyberdyne",
				start:    "2026-10-10T12:00:00Z",
				units:    123_456_789,
			},
			{
				customer: "acme",
				start:    "2026-10-20T12:00:00Z",
				units:    200_000_000,
			},
			{
				customer: "cyberdyne",
				start:    "2026-10-20T12:00:00Z",
				units:    200_000_000,
			},
			{
				customer: "acme",
				start:    "2026-10-30T12:00:00Z",
				units:    50_000_000,
			},
			{
				customer: "acme",
				start:    "2026-11-03T12:00:00Z",
				units:    100_000_000,
			},
		}
		replay := struct {
			arguments             []string
			wantFailure           bool
			wantSnapshotUnchanged bool
		}{arguments: []string{"--action=replay", "--scenario=duplicates", "--batch-size=1", "--reverse=true"}, wantFailure: false, wantSnapshotUnchanged: true}

		pool, source := apiFixture(t)
		binary := simulatorExecutable(t)
		state := filepath.Join(t.TempDir(), "run.json")
		base := []string{"--state=" + state, "--api-url=" + apiURL(), "--source=" + source, "--retry-min=1ms", "--retry-max=2ms"}
		var before string
		for _, tt := range steps {
			t.Run(tt.name, func(t *testing.T) {
				output := simulatorCommand(t, binary, tt.wantFailure, append(base, tt.arguments...)...)
				if !strings.Contains(output, tt.wantOutput) {
					t.Errorf("Output = %s; want text %q", output, tt.wantOutput)
				}
				if count := apiEventCount(t, pool, source); count != tt.wantRows {
					t.Fatalf("Committed rows = %d; want %d", count, tt.wantRows)
				}
				after := apiSnapshot(t, pool, source)
				if tt.wantSnapshotUnchanged && after != before {
					t.Error("Restart changed committed measurements or receipts")
				}
				before = after
			})
		}
		rows, err := pool.Query(context.Background(), `
	        SELECT customer_id, period_start, period_end, units, metric
	        FROM usage_inbox WHERE source = $1 ORDER BY period_start, customer_id`, source)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		index := 0
		for rows.Next() {
			var customer, metric string
			var start, end time.Time
			var units int64
			if err := rows.Scan(&customer, &start, &end, &units, &metric); err != nil {
				t.Fatal(err)
			}
			if index >= len(wantAssignment) {
				t.Fatal("Simulator inserted extra assignment events")
			}
			expected := wantAssignment[index]
			if customer != expected.customer || start.UTC().Format(time.RFC3339) != expected.start ||
				end.Sub(start) != time.Hour || units != expected.units || metric != "cpu_seconds" {
				t.Errorf("Committed fixture %d = %s %s %s %d %s", index, customer, start, end, units, metric)
			}
			index++
		}
		if err := rows.Err(); err != nil || index != len(wantAssignment) {
			t.Fatalf("Assignment rows = %d, %v", index, err)
		}
		rows.Close()
		before = apiSnapshot(t, pool, source)
		simulatorCommand(t, binary, replay.wantFailure, append(base, replay.arguments...)...)
		if after := apiSnapshot(t, pool, source); (after == before) != replay.wantSnapshotUnchanged {
			t.Error("Repeated executable replay changed the stored inbox")
		}
	})
	t.Run("simulator executable offline buffer", func(t *testing.T) {
		steps := []struct {
			name        string
			arguments   []string
			useLiveAPI  bool
			wantFailure bool
			wantOutput  string
			wantRows    int
		}{
			{
				name:        "generate without billing",
				arguments:   []string{"--action=generate"},
				wantFailure: false,
				wantRows:    0,
			},
			{
				name:        "retain buffer while API is offline",
				arguments:   []string{"--action=send", "--api-url=http://127.0.0.1:1", "--max-attempts=1"},
				wantFailure: true,
				wantOutput:  "pending=2 delivered=0",
				wantRows:    0,
			},
			{
				name:        "resume against the API",
				arguments:   []string{"--action=send"},
				useLiveAPI:  true,
				wantFailure: false,
				wantRows:    2,
			},
			{
				name:        "send confirmed buffer again",
				arguments:   []string{"--action=send"},
				useLiveAPI:  true,
				wantFailure: false,
				wantRows:    2,
			},
		}
		pool, source := apiFixture(t)
		binary := simulatorExecutable(t)
		state := filepath.Join(t.TempDir(), "run.json")
		base := []string{"--state=" + state, "--source=" + source, "--mode=step"}
		for _, tt := range steps {
			t.Run(tt.name, func(t *testing.T) {
				arguments := append(base, tt.arguments...)
				if tt.useLiveAPI {
					arguments = append(arguments, "--api-url="+apiURL())
				}
				output := simulatorCommand(t, binary, tt.wantFailure, arguments...)
				if !strings.Contains(output, tt.wantOutput) {
					t.Errorf("Output = %s; want text %q", output, tt.wantOutput)
				}
				if count := apiEventCount(t, pool, source); count != tt.wantRows {
					t.Fatalf("Committed rows = %d; want %d", count, tt.wantRows)
				}
			})
		}
	})
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
