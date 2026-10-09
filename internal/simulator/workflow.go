package simulator

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
		if step.Request.Method != "GET" && step.Request.Method != "POST" {
			return fmt.Errorf("step %s: method must be GET or POST", step.Name)
		}
		path, err := url.ParseRequestURI(step.Request.Path)
		if err != nil || path.IsAbs() || path.Host != "" || !strings.HasPrefix(step.Request.Path, "/") || strings.HasPrefix(step.Request.Path, "//") || strings.Contains(step.Request.Path, "#") || step.Want.Status < 100 || step.Want.Status > 599 {
			return fmt.Errorf("step %s: invalid relative path or expected status", step.Name)
		}
		if step.Await && step.Request.Method != "GET" {
			return fmt.Errorf("step %s: only reads can await accounting", step.Name)
		}
		if step.Want.SameAs != "" && !captures[step.Want.SameAs] {
			return fmt.Errorf("step %s: response capture must precede its comparison", step.Name)
		}
		if step.Capture != "" {
			if captures[step.Capture] {
				return fmt.Errorf("step %s: capture names must be unique", step.Name)
			}
			captures[step.Capture] = true
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
	if o.StatePath == "" || strings.TrimSpace(o.Source) == "" || !utf8.ValidString(o.Source) || strings.ContainsRune(o.Source, 0) || len(o.Source) > 256 {
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
	address, err := url.Parse(o.BaseURL)
	if err != nil || (address.Scheme != "http" && address.Scheme != "https") || address.Host == "" || address.User != nil || address.RawQuery != "" || address.Fragment != "" {
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
