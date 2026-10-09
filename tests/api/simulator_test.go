//go:build integration

package api_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type simulatorStep struct {
	name               string
	arguments          []string
	wantError          bool
	wantCount          int
	wantOutput         string
	wantUnchangedInbox bool
}

type storedMeasurement struct {
	customer string
	start    string
	end      string
	units    int64
	metric   string
}

// TestSimulatorExecutable verifies one durable transport workflow per named
// scenario. Every command and its expected inbox/CLI result appear before execution.
func TestSimulatorExecutable(t *testing.T) {
	cases := []struct {
		name             string
		steps            []simulatorStep
		wantMeasurements []storedMeasurement
	}{
		{
			name: "lost acknowledgement, restart, assignment barrier, and replay",
			steps: []simulatorStep{
				{
					name:       "lost response retains already committed measurements",
					arguments:  []string{"--scenario=lost-response", "--mode=step", "--max-attempts=1"},
					wantError:  true,
					wantCount:  2,
					wantOutput: "generated=2 pending=2 delivered=0",
				},
				{
					name:               "new process confirms regrouped retry",
					arguments:          []string{"--action=send", "--batch-size=1"},
					wantError:          false,
					wantCount:          2,
					wantUnchangedInbox: true,
				},
				{
					name:      "finish October phase",
					arguments: []string{"--mode=fast"},
					wantError: false,
					wantCount: 4,
				},
				{
					name:               "barrier retains late October",
					arguments:          []string{"--mode=fast"},
					wantError:          false,
					wantCount:          4,
					wantOutput:         "Waiting at barrier",
					wantUnchangedInbox: true,
				},
				{
					name:      "operator releases late October and November",
					arguments: []string{"--advance=true", "--mode=fast"},
					wantError: false,
					wantCount: 6,
				},
				{
					name:               "reversed repeated replay preserves the entire inbox",
					arguments:          []string{"--action=replay", "--scenario=duplicates", "--batch-size=1", "--reverse=true"},
					wantError:          false,
					wantCount:          6,
					wantUnchangedInbox: true,
				},
			},
			wantMeasurements: []storedMeasurement{
				{
					customer: "acme",
					start:    "2026-10-10T12:00:00Z",
					end:      "2026-10-10T13:00:00Z",
					units:    100_000_000,
					metric:   "cpu_seconds",
				},
				{
					customer: "cyberdyne",
					start:    "2026-10-10T12:00:00Z",
					end:      "2026-10-10T13:00:00Z",
					units:    123_456_789,
					metric:   "cpu_seconds",
				},
				{
					customer: "acme",
					start:    "2026-10-20T12:00:00Z",
					end:      "2026-10-20T13:00:00Z",
					units:    200_000_000,
					metric:   "cpu_seconds",
				},
				{
					customer: "cyberdyne",
					start:    "2026-10-20T12:00:00Z",
					end:      "2026-10-20T13:00:00Z",
					units:    200_000_000,
					metric:   "cpu_seconds",
				},
				{
					customer: "acme",
					start:    "2026-10-30T12:00:00Z",
					end:      "2026-10-30T13:00:00Z",
					units:    50_000_000,
					metric:   "cpu_seconds",
				},
				{
					customer: "acme",
					start:    "2026-11-03T12:00:00Z",
					end:      "2026-11-03T13:00:00Z",
					units:    100_000_000,
					metric:   "cpu_seconds",
				},
			},
		},
		{
			name: "offline generation survives a failed delivery process",
			steps: []simulatorStep{
				{
					name:      "generate only the first phase without HTTP",
					arguments: []string{"--action=generate", "--mode=step"},
					wantError: false,
					wantCount: 0,
				},
				{
					name:       "unavailable API retains the buffer",
					arguments:  []string{"--action=send", "--api-url=http://127.0.0.1:1", "--max-attempts=1"},
					wantError:  true,
					wantCount:  0,
					wantOutput: "pending=2 delivered=0",
				},
				{
					name:      "restart against the available API",
					arguments: []string{"--action=send"},
					wantError: false,
					wantCount: 2,
				},
				{
					name:               "confirmed buffer sends nothing again",
					arguments:          []string{"--action=send"},
					wantError:          false,
					wantCount:          2,
					wantUnchangedInbox: true,
				},
			},
		},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			pool, source := apiFixture(t)
			binary := simulatorExecutable(t)
			state := filepath.Join(t.TempDir(), "run.json")
			base := []string{"--state=" + state, "--api-url=" + apiURL(), "--source=" + source, "--retry-min=1ms", "--retry-max=2ms"}
			for _, step := range scenario.steps {
				t.Run(step.name, func(t *testing.T) {
					var before string
					if step.wantUnchangedInbox {
						before = apiSnapshot(t, pool, source)
					}
					output := simulatorCommand(t, binary, step.wantError, append(append([]string(nil), base...), step.arguments...)...)
					if actual := apiEventCount(t, pool, source); actual != step.wantCount {
						t.Fatalf("command %v: inbox count=%d want %d; output=%s", step.arguments, actual, step.wantCount, output)
					}
					if step.wantOutput != "" && !strings.Contains(output, step.wantOutput) {
						t.Fatalf("command %v: output=%s want fragment=%s", step.arguments, output, step.wantOutput)
					}
					if step.wantUnchangedInbox && apiSnapshot(t, pool, source) != before {
						t.Fatalf("command %v changed acknowledged measurements or receipt metadata", step.arguments)
					}
				})
				if t.Failed() {
					return
				}
			}
			if scenario.wantMeasurements != nil {
				assertSimulatorMeasurements(t, pool, source, scenario.wantMeasurements)
			}
		})
	}
}

// assertSimulatorMeasurements reads only the scenario's namespace and compares
// every stored measurement to its explicit literal customer, times, units, and metric.
func assertSimulatorMeasurements(t *testing.T, pool *pgxpool.Pool, source string, want []storedMeasurement) {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
        SELECT customer_id, period_start, period_end, units, metric
        FROM usage_inbox
        WHERE source = $1
        ORDER BY period_start, customer_id`, source)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actual []storedMeasurement
	for rows.Next() {
		var item storedMeasurement
		var start, end time.Time
		if err := rows.Scan(&item.customer, &start, &end, &item.units, &item.metric); err != nil {
			t.Fatal(err)
		}
		item.start = start.UTC().Format(time.RFC3339)
		item.end = end.UTC().Format(time.RFC3339)
		actual = append(actual, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("stored measurements=%+v want %+v", actual, want)
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
