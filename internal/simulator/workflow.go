package simulator

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// Workflow is a public-API scenario with visible inputs and literal assertions.
// It knows transport and expected results; it performs no billing calculation.
type Workflow struct {
	Name  string         `json:"name"`
	Steps []WorkflowStep `json:"steps"`
}

type WorkflowStep struct {
	Name    string              `json:"name"`
	Request WorkflowRequest     `json:"request"`
	Want    WorkflowExpectation `json:"want"`
	Await   bool                `json:"await,omitempty"`
	Capture string              `json:"capture,omitempty"`
	Fault   WorkflowFault       `json:"fault,omitempty"`
}

type WorkflowFault struct {
	LoseResponse bool `json:"lose_response,omitempty"`
}

type WorkflowOptions struct {
	Plan         Workflow
	StatePath    string
	Source       string
	BaseURL      string
	Client       *http.Client
	Action       string
	Mode         string
	RetryMin     time.Duration
	RetryMax     time.Duration
	MaxAttempts  int
	LoseResponse bool
	Output       io.Writer
}

func LoadWorkflow(path string) (Workflow, error) {
	file, err := os.Open(path)
	if err != nil {
		return Workflow{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil {
		return Workflow{}, err
	}
	if len(data) > 4<<20 {
		return Workflow{}, fmt.Errorf("workflow exceeds four MiB")
	}
	var plan Workflow
	if err := decodeStrict(strings.NewReader(string(data)), &plan); err != nil {
		return plan, err
	}
	return plan, validateWorkflow(plan)
}

func validateWorkflow(plan Workflow) error {
	if strings.TrimSpace(plan.Name) == "" || len(plan.Steps) == 0 {
		return fmt.Errorf("workflow requires a name and steps")
	}
	names := map[string]bool{}
	captures := map[string]bool{}
	for _, step := range plan.Steps {
		if strings.TrimSpace(step.Name) == "" || names[step.Name] {
			return fmt.Errorf("workflow step names must be nonblank and unique")
		}
		names[step.Name] = true
		if err := validateWorkflowStep(step); err != nil {
			return err
		}
		if err := registerWorkflowCapture(step, captures); err != nil {
			return err
		}
	}
	return nil
}

// RunWorkflow holds the producer lock and checkpoints every observed step. A
// crash before a checkpoint repeats the same public operation identity/content.
func RunWorkflow(ctx context.Context, options WorkflowOptions) error {
	if err := validateWorkflowOptions(options); err != nil {
		return err
	}
	if err := validateWorkflow(options.Plan); err != nil {
		return err
	}
	if options.Output == nil {
		options.Output = io.Discard
	}
	store, err := OpenStore(options.StatePath)
	if err != nil {
		return err
	}
	defer store.Close()
	state, err := openWorkflowState(store, options)
	if err != nil {
		return err
	}
	if options.Action == "status" {
		printWorkflowStatus(options.Output, state)
		return nil
	}
	runner := workflowRunner{options: options, store: store, state: state}
	defer printWorkflowStatus(options.Output, state)
	for state.NextStep < len(options.Plan.Steps) {
		step := options.Plan.Steps[state.NextStep]
		if err := runner.runStep(ctx, step); err != nil {
			return err
		}
		if options.Mode == "step" {
			return nil
		}
	}
	return nil
}

func validateWorkflowOptions(o WorkflowOptions) error {
	if o.StatePath == "" || !validWorkflowSource(o.Source) {
		return fmt.Errorf("workflow requires a state file and stable source")
	}
	if o.Action != "run" && o.Action != "send" && o.Action != "status" {
		return fmt.Errorf("workflow action must be run, send, or status")
	}
	if o.Mode != "fast" && o.Mode != "step" {
		return fmt.Errorf("workflow mode must be fast or step")
	}
	if o.Action == "status" {
		return nil
	}
	return validateWorkflowTransport(o)
}

func validWorkflowSource(source string) bool {
	return strings.TrimSpace(source) != "" && utf8.ValidString(source) &&
		!strings.ContainsRune(source, 0) && len(source) <= 256
}

func validateWorkflowTransport(o WorkflowOptions) error {
	if !validBaseURL(o.BaseURL) {
		return fmt.Errorf("workflow requires a plain HTTP(S) base URL")
	}
	if o.Client == nil || o.RetryMin <= 0 || o.RetryMax < o.RetryMin || o.MaxAttempts < 0 {
		return fmt.Errorf("workflow requires a client and valid retry settings")
	}
	return nil
}

func printWorkflowStatus(output io.Writer, state *workflowState) {
	fmt.Fprintf(output, "workflow=%s completed=%d steps=%d attempts=%d\n", state.Name, state.NextStep, state.StepCount, state.Attempts)
	if state.LastError != "" {
		fmt.Fprintf(output, "Last workflow error: %s\n", state.LastError)
	}
}
