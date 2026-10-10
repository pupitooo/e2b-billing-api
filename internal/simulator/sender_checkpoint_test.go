package simulator

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestCheckpointDeliveryReceipt verifies durable acknowledgement separately from
// HTTP transport. Each isolated case starts with one confirmed and one pending
// event; a failed save must restore both flags and preserve the previous disk state.
func TestCheckpointDeliveryReceipt(t *testing.T) {
	cases := []struct {
		name                  string
		delivered             []bool
		batch                 []int
		lastError             string
		storageUnavailable    bool
		wantDelivered         []bool
		wantLastError         string
		wantPersistedDelivery []bool
		wantPersistedError    string
		wantError             bool
		wantErrorPrefix       string
	}{
		{
			name:      "acknowledged replay and new event are saved together",
			delivered: []bool{true, false}, batch: []int{0, 1}, lastError: "previous failure",
			wantDelivered: []bool{true, true}, wantLastError: "",
			wantPersistedDelivery: []bool{true, true}, wantPersistedError: "",
		},
		{
			name:      "failed save restores existing confirmation and leaves the new event pending",
			delivered: []bool{true, false}, batch: []int{0, 1}, lastError: "previous failure",
			storageUnavailable:    true,
			wantDelivered:         []bool{true, false},
			wantLastError:         "Delivery receipt could not be saved; retain and retry the original measurements.",
			wantPersistedDelivery: []bool{true, false}, wantPersistedError: "previous failure",
			wantError: true, wantErrorPrefix: "save delivery receipt: ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Only the two released events matter to checkpoint persistence.
			// Their transport identifiers come from a private valid plan fixture.
			plan := simulatorPlan(t)
			plan.Steps = plan.Steps[:1]
			state := &State{
				Version: 1, Plan: plan, NextStep: 1,
				Delivered: append([]bool(nil), tc.delivered...), LastError: tc.lastError,
			}
			directory := filepath.Join(t.TempDir(), "sender")
			store, err := OpenStore(filepath.Join(directory, "state.json"))
			if err != nil {
				t.Fatalf("Open private checkpoint store: %v", err)
			}
			defer store.Close()
			if err := store.Save(state); err != nil {
				t.Fatalf("Save initial delivery flags %v: %v", tc.delivered, err)
			}

			if tc.storageUnavailable {
				if err := os.Rename(directory, directory+"-offline"); err != nil {
					t.Fatalf("Make checkpoint storage unavailable: %v", err)
				}
			}

			err = store.checkpointDeliveryReceipt(state, tc.batch)
			if tc.wantError {
				if err == nil {
					t.Fatalf("checkpointDeliveryReceipt(batch=%v, delivered=%v) error = nil; want prefix %q", tc.batch, tc.delivered, tc.wantErrorPrefix)
				}

				if !strings.HasPrefix(err.Error(), tc.wantErrorPrefix) {
					t.Errorf("checkpointDeliveryReceipt(batch=%v) error = %q; want prefix %q", tc.batch, err, tc.wantErrorPrefix)
				}
			} else if err != nil {
				t.Fatalf("checkpointDeliveryReceipt(batch=%v, delivered=%v) error = %v; want nil", tc.batch, tc.delivered, err)
			}

			if !reflect.DeepEqual(state.Delivered, tc.wantDelivered) || state.LastError != tc.wantLastError {
				t.Errorf("checkpointDeliveryReceipt(batch=%v) in-memory flags=%v error=%q; want flags=%v error=%q", tc.batch, state.Delivered, state.LastError, tc.wantDelivered, tc.wantLastError)
			}

			if tc.storageUnavailable {
				if err := os.Rename(directory+"-offline", directory); err != nil {
					t.Fatalf("Restore checkpoint storage for verification: %v", err)
				}
			}

			persisted, err := store.Load()
			if err != nil {
				t.Fatalf("Reload checkpoint after receipt attempt: %v", err)
			}

			if !reflect.DeepEqual(persisted.Delivered, tc.wantPersistedDelivery) || persisted.LastError != tc.wantPersistedError {
				t.Errorf("checkpointDeliveryReceipt(batch=%v) persisted flags=%v error=%q; want flags=%v error=%q", tc.batch, persisted.Delivered, persisted.LastError, tc.wantPersistedDelivery, tc.wantPersistedError)
			}
		})
	}
}
