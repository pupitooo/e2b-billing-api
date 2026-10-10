package simulator

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Store.Load recovers the complete saved checkpoint after reopening and ignores
// an interrupted replacement file while retaining the expected pending event.
func TestStoreLoad(t *testing.T) {
	t.Run("simulator store recovery", func(t *testing.T) {
		tt := struct {
			nextStep            int
			attempts            uint64
			deliveredIndex      int
			interruptedSnapshot string
			wantLoadedUnchanged bool
			wantPending         int
		}{
			nextStep:            1,
			attempts:            1,
			deliveredIndex:      0,
			interruptedSnapshot: `{"version":`,
			wantLoadedUnchanged: true,
			wantPending:         1,
		}

		path := filepath.Join(t.TempDir(), "run.json")
		store, err := OpenStore(path)
		if err != nil {
			t.Fatal(err)
		}

		plan := simulatorPlan(t)
		state := &State{Version: 1, Plan: plan, NextStep: tt.nextStep, Delivered: make([]bool, 6), Attempts: tt.attempts}
		state.Delivered[tt.deliveredIndex] = true
		if err := store.Save(state); err != nil {
			t.Fatal(err)
		}

		if err := store.Close(); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(filepath.Dir(path), ".simulator-interrupted"), []byte(tt.interruptedSnapshot), 0600); err != nil {
			t.Fatal(err)
		}

		reopened, err := OpenStore(path)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		loaded, err := reopened.Load()
		if err != nil || sameJSON(state, loaded) != tt.wantLoadedUnchanged || len(loaded.pending(false)) != tt.wantPending {
			t.Fatalf("Recovery = %+v, %v", loaded, err)
		}
	})
}

// OpenStore excludes a competing process and acquires the released OS lock
// after its owner is killed, retaining that owner's durable pending checkpoint.
func TestOpenStore(t *testing.T) {
	t.Run("concurrent owners of one path", func(t *testing.T) {
		tt := struct {
			path      string
			wantError bool
		}{path: filepath.Join(t.TempDir(), "run.json"), wantError: true}
		owner, err := OpenStore(tt.path)
		if err != nil {
			t.Fatal(err)
		}
		defer owner.Close()

		competing, err := OpenStore(tt.path)
		if competing != nil {
			defer competing.Close()
		}

		if (err != nil) != tt.wantError {
			t.Errorf("OpenStore(%q) error = %v; want error=%t", tt.path, err, tt.wantError)
		}
	})

	t.Run("simulator killed process releases lock", func(t *testing.T) {
		tt := struct {
			helperFilter       string
			wantReadyOutput    string
			wantCompetingError bool
			wantRecoveredStep  int
			wantPending        int
		}{
			helperFilter:       "-test.run=^TestSimulatorLockProcess$",
			wantReadyOutput:    "locked\n",
			wantCompetingError: true,
			wantRecoveredStep:  1,
			wantPending:        2,
		}

		path := filepath.Join(t.TempDir(), "run.json")
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, tt.helperFilter)
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
		if err != nil || line != tt.wantReadyOutput {
			t.Fatalf("Lock owner did not become ready: %q, %v", line, err)
		}

		if competing, err := OpenStore(path); (err != nil) != tt.wantCompetingError {
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
		if err != nil || state.NextStep != tt.wantRecoveredStep || len(state.pending(false)) != tt.wantPending {
			t.Fatalf("Killed-owner recovery = %+v, %v", state, err)
		}
	})
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

// Store.Save refuses a delivery receipt for an event whose generation step has
// not been released, preventing an invalid checkpoint from replacing the file.
func TestStoreSave(t *testing.T) {
	plan := simulatorPlan(t)
	tests := []struct {
		name      string
		state     State
		wantError bool
	}{
		{
			name:      "receipt for unreleased event",
			state:     State{Version: 1, Plan: plan, NextStep: 0, Delivered: []bool{true, false, false, false, false, false}, Attempts: 1},
			wantError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := OpenStore(filepath.Join(t.TempDir(), "run.json"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			err = store.Save(&tt.state)
			if (err != nil) != tt.wantError {
				t.Errorf("Save(%+v) error = %v; want error=%t", tt.state, err, tt.wantError)
			}
		})
	}
}
