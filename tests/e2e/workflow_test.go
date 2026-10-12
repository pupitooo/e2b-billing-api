//go:build integration

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"e2b/billing-api/internal/simulator"
)

type producerExpectation struct {
	wantError         bool
	wantRetainedError bool
	wantOutput        string
	wantCompleted     int
	wantSteps         int
}

// loadWorkflow reads the same reviewed public inputs and literal financial
// expectations as the manual CLI; no domain calculation prepares its outputs.
func loadWorkflow(t *testing.T, file string) simulator.Workflow {
	t.Helper()
	plan, err := simulator.LoadWorkflow(filepath.Join("../../docs/simulator", file+".json"))
	if err != nil {
		t.Fatalf("Load public workflow %s: %v", file, err)
	}
	return plan
}

// producer launches a fresh executable with a persistent checkpoint, verifies
// its process outcome independently, and retains output and checkpoint evidence.
func (e *environment) producer(t *testing.T, file string, want producerExpectation, extra ...string) {
	t.Helper()
	state := filepath.Join(e.evidence, file+"-state.json")
	args := []string{"--scenario=" + file, "--file=" + filepath.Join(e.root, "docs/simulator", file+".json"), "--state=" + state, "--source=e2e-workflow", "--api-url=http://api:8080", "--mode=step", "--retry-min=20ms", "--retry-max=100ms", "--max-attempts=200"}
	args = append(args, extra...)
	ctx, cancel := context.WithTimeout(context.Background(), observationTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, "platform-simulator", args...).CombinedOutput()
	e.save(t, "producer.log", fmt.Sprintf("%v\n%s\n", args, output), true)
	if ctx.Err() != nil {
		t.Fatalf("Producer %s exceeded deadline: %v\n%s", file, ctx.Err(), output)
	}
	if (err != nil) != want.wantError {
		t.Fatalf("Producer %s error=%v; wantError=%v\n%s", file, err, want.wantError, output)
	}
	if want.wantOutput != "" && !strings.Contains(string(output), want.wantOutput) {
		t.Fatalf("Producer %s output=%s; want fragment %q", file, output, want.wantOutput)
	}
	e.assertCheckpoint(t, state, want)
}

// assertCheckpoint checks cursor, plan length, and retained failure independently
// of the CLI's summary; a failing operation must never advance producer progress.
func (e *environment) assertCheckpoint(t *testing.T, path string, want producerExpectation) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Read producer checkpoint %s: %v", path, err)
	}
	var actual struct {
		NextStep  int    `json:"next_step"`
		StepCount int    `json:"step_count"`
		LastError string `json:"last_error"`
	}
	if err := json.Unmarshal(data, &actual); err != nil {
		t.Fatalf("Decode checkpoint %s: %v", path, err)
	}
	if actual.NextStep != want.wantCompleted || actual.StepCount != want.wantSteps || (actual.LastError != "") != want.wantRetainedError {
		t.Fatalf("Checkpoint %s=%+v; want completed=%d steps=%d retainedError=%v", path, actual, want.wantCompleted, want.wantSteps, want.wantRetainedError)
	}
}

// workflowStep executes one declared public operation. Lost replies deliberately
// end the first producer, then a new producer confirms the original operation;
// complete database rows must be unchanged by that retry.
func (e *environment) workflowStep(t *testing.T, file string, index int, step simulator.WorkflowStep, total int) {
	t.Helper()
	if step.Fault.LoseResponse {
		e.producer(t, file, producerExpectation{wantError: true, wantRetainedError: true, wantOutput: "injected lost committed response", wantCompleted: index, wantSteps: total}, "--max-attempts=1")
		e.awaitSQL(t, "committed fault finishes accounting", `SELECT count(*) FROM usage_inbox WHERE processed_at IS NULL AND processing_error IS NULL`, "0")
		before := e.snapshot(t)
		e.producer(t, file, producerExpectation{wantError: false, wantOutput: "Passed step", wantCompleted: index + 1, wantSteps: total})
		if after := e.snapshot(t); after != before {
			t.Fatalf("Workflow %s step %s retry changed financial/audit rows: before=%s after=%s", file, step.Name, before, after)
		}
		return
	}
	e.producer(t, file, producerExpectation{wantError: false, wantOutput: "Passed step", wantCompleted: index + 1, wantSteps: total})
}

// runWorkflow gives every step its own named subtest, advances the real service
// configuration at declared boundaries, and stops rather than cascading after
// a failed prerequisite. Completed restart/status commands must change no rows.
func runWorkflow(t *testing.T, scenario workflowScenario) {
	plan := loadWorkflow(t, scenario.file)
	if len(plan.Steps) != scenario.wantSteps {
		t.Fatalf("Workflow %s steps=%d; want %d", scenario.file, len(plan.Steps), scenario.wantSteps)
	}
	e := newEnvironment(t, scenario.file)
	e.migrate(t)
	e.start(t, scenario.initialTime)
	for index, step := range plan.Steps {
		passed := t.Run(step.Name, func(t *testing.T) {
			now := step.BusinessTime
			if declared, found := scenario.clockChanges[step.Name]; found {
				now = declared
			}
			if now != "" {
				e.advance(t, now)
			}
			var before string
			if step.Want.Status >= 400 {
				before = e.snapshot(t)
			}
			e.workflowStep(t, scenario.file, index, step, scenario.wantSteps)
			if before != "" && e.snapshot(t) != before {
				t.Fatalf("Rejected workflow step %s changed financial or audit rows", step.Name)
			}
		})
		if !passed {
			return
		}
	}
	e.assertInvariants(t)
	before := e.snapshot(t)
	e.save(t, "final-state.json", before, false)
	e.producer(t, scenario.file, producerExpectation{wantCompleted: scenario.wantSteps, wantSteps: scenario.wantSteps}, "--action=status")
	e.producer(t, scenario.file, producerExpectation{wantCompleted: scenario.wantSteps, wantSteps: scenario.wantSteps})
	if after := e.snapshot(t); after != before {
		t.Fatalf("Completed workflow %s changed rows on status/restart", scenario.file)
	}
}
