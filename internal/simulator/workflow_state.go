package simulator

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
)

type workflowState struct {
	Version              int                        `json:"version"`
	Name                 string                     `json:"name"`
	Source               string                     `json:"source"`
	Fingerprint          string                     `json:"fingerprint"`
	StepCount            int                        `json:"step_count"`
	NextStep             int                        `json:"next_step"`
	Attempts             uint64                     `json:"attempts"`
	Captures             map[string]json.RawMessage `json:"captures"`
	LostSteps            map[int]bool               `json:"lost_steps"`
	LostResponseInjected bool                       `json:"lost_response_injected"`
	LastError            string                     `json:"last_error,omitempty"`
}

func openWorkflowState(store *Store, options WorkflowOptions) (*workflowState, error) {
	encoded, _ := json.Marshal(options.Plan)
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(encoded))
	state := &workflowState{Version: 1, Name: options.Plan.Name, Source: options.Source, Fingerprint: fingerprint,
		StepCount: len(options.Plan.Steps), Captures: map[string]json.RawMessage{}, LostSteps: map[int]bool{}}
	data, err := os.ReadFile(store.path)
	if os.IsNotExist(err) && options.Action != "status" {
		return state, store.saveJSON(state)
	}
	if err != nil {
		return nil, err
	}
	loaded := new(workflowState)
	if err := decodeStrict(bytes.NewReader(data), loaded); err != nil {
		return nil, fmt.Errorf("invalid workflow state; retain it for investigation: %w", err)
	}
	state = loaded
	if state.Version != 1 || state.Name != options.Plan.Name || state.Fingerprint != fingerprint || state.Source != options.Source || state.NextStep < 0 || state.NextStep > len(options.Plan.Steps) || state.StepCount != len(options.Plan.Steps) || state.Captures == nil || state.LostSteps == nil {
		return nil, fmt.Errorf("workflow differs from saved state; retain its original plan, source, and file")
	}
	return state, nil
}
