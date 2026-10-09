package simulator

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSimulatorStoreRecovery preserves released pending events and receipts
// through reopening. An interrupted temporary replacement cannot replace the
// complete snapshot, and a second command cannot concurrently own its lock.
func TestSimulatorStoreRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	plan := simulatorPlan(t)
	state := &State{Version: 1, Plan: plan, NextStep: 1, Delivered: make([]bool, 6), Attempts: 1}
	state.Delivered[0] = true
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	if second, err := OpenStore(path); err == nil {
		second.Close()
		t.Error("Concurrent state owner obtained the lock")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), ".simulator-interrupted"), []byte(`{"version":`), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err := reopened.Load()
	if err != nil || !sameJSON(state, loaded) || len(loaded.pending(false)) != 1 {
		t.Fatalf("Recovery = %+v, %v", loaded, err)
	}
	loaded.NextStep = 0
	if err := reopened.Save(loaded); err == nil {
		t.Error("Saved a receipt for an unreleased event")
	}
}

// TestSimulatorCorruptStateIsRetained proves a new run cannot overwrite a
// truncated or unsupported snapshot; potentially recoverable evidence remains.
func TestSimulatorCorruptStateIsRetained(t *testing.T) {
	for _, data := range []string{`{"version":`, `{"version":2}`} {
		path := filepath.Join(t.TempDir(), "run.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		plan := simulatorPlan(t)
		err := Run(context.Background(), Options{StatePath: path, Action: "generate", Mode: "fast", Plan: &plan})
		if err == nil {
			t.Error("Corrupt state was replaced")
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != data {
			t.Fatalf("Original state was not retained: %s, %v", after, err)
		}
	}
}

// TestSimulatorStatusDuringDelivery reads a complete snapshot while another
// command owns the writer lock. Operators must be able to inspect the retained
// buffer during retries without interrupting delivery or modifying its state.
func TestSimulatorStatusDuringDelivery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state := &State{Version: 1, Plan: simulatorPlan(t), NextStep: 1, Delivered: make([]bool, 6)}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Run(context.Background(), Options{StatePath: path, Action: "status", Mode: "fast", Output: &output}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || !strings.Contains(output.String(), "generated=2 pending=2 delivered=0") {
		t.Fatalf("Concurrent status changed or misreported the buffer: %s, %v", output.String(), err)
	}
}

// TestSimulatorChangedPlanCannotReplacePending rejects a different namespace
// or payload on restart, preserving the original buffer byte for byte.
func TestSimulatorChangedPlanCannotReplacePending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	plan := simulatorPlan(t)
	options := Options{StatePath: path, Action: "generate", Mode: "step", Plan: &plan}
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := Assignment("changed-source", 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	options.Plan = &changed
	if err := Run(context.Background(), options); err == nil {
		t.Error("Changed plan replaced pending work")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Error("Changed input mutated the original state")
	}
}

// TestSimulatorKilledProcessReleasesLock kills an independent state owner,
// then recovers its synced plan and obtains the automatically released OS lock.
func TestSimulatorKilledProcessReleasesLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "-test.run=^TestSimulatorLockProcess$")
	command.Env = append(os.Environ(), "E2B_SIMULATOR_LOCK_HELPER="+path)
	pipe, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	line, err := bufio.NewReader(pipe).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("Lock owner did not become ready: %q, %v", line, err)
	}
	if competing, err := OpenStore(path); err == nil {
		competing.Close()
		t.Error("Another process's lock was ignored")
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	command.Wait()
	recovered, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	state, err := recovered.Load()
	if err != nil || state.NextStep != 1 || len(state.pending(false)) != 2 {
		t.Fatalf("Killed-owner recovery = %+v, %v", state, err)
	}
}

// TestSimulatorAdvancePassesOnlyOneBarrier preserves later operator pauses in
// a custom workflow: fast mode may pass one explicitly authorized barrier per
// command, while the next barrier remains held until a separate advancement.
func TestSimulatorAdvancePassesOnlyOneBarrier(t *testing.T) {
	plan := simulatorPlan(t)
	plan.Steps[1].Barrier = "First operator pause."
	path := filepath.Join(t.TempDir(), "run.json")
	options := Options{StatePath: path, Action: "generate", Mode: "fast", Advance: true, Plan: &plan}
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if cursor := readSimulatorState(t, path).NextStep; cursor != 2 {
		t.Fatalf("One advance passed multiple barriers: cursor=%d", cursor)
	}
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if cursor := readSimulatorState(t, path).NextStep; cursor != 4 {
		t.Errorf("Separate advance did not release the next phase: cursor=%d", cursor)
	}
}

// TestSimulatorLockProcess is a subprocess-only helper: it saves pending
// assignment events, announces its held lock, and waits to be forcibly killed.
func TestSimulatorLockProcess(t *testing.T) {
	path := os.Getenv("E2B_SIMULATOR_LOCK_HELPER")
	if path == "" {
		return
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	plan := simulatorPlan(t)
	state := &State{Version: 1, Plan: plan, NextStep: 1, Delivered: make([]bool, 6)}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("locked\n")
	<-time.After(time.Hour)
}

// readSimulatorState inspects the on-disk checkpoint independently of the
// in-memory sender, detecting missing durable release or receipt writes.
func readSimulatorState(t *testing.T, path string) *State {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if err := state.validate(); err != nil {
		t.Fatal(err)
	}
	return &state
}
