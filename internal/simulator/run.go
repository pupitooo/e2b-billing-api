package simulator

import (
	"context"
	"fmt"
	"io"
	"os"
)

// Options separates releasing a scenario from sending its durable buffer.
type Options struct {
	StatePath string
	Action    string
	Mode      string
	Advance   bool
	Plan      *Plan
	Sender    *Sender
	Output    io.Writer
}

// Run resumes saved work before releasing another step. A changed input plan
// is rejected so restart cannot silently replace measurement content or IDs.
func Run(ctx context.Context, options Options) error {
	if options.Output == nil {
		options.Output = io.Discard
	}

	if err := options.validate(); err != nil {
		return err
	}

	if options.Action == "status" {
		return readRunStatus(options)
	}

	store, err := OpenStore(options.StatePath)
	if err != nil {
		return err
	}
	defer store.Close()
	state, err := resumeRun(store, options)
	if err != nil {
		return err
	}
	defer printStatus(options.Output, state)
	if options.generates() {
		if err := releaseScenario(ctx, store, state, options); err != nil {
			return err
		}
	}

	if options.delivers() {
		return options.Sender.Send(ctx, store, state, options.Action == "replay")
	}

	return nil
}

func (options Options) validate() error {
	if options.StatePath == "" || (options.Mode != "fast" && options.Mode != "step") {
		return fmt.Errorf("state is required and mode must be fast or step")
	}

	switch options.Action {
	case "run", "generate", "send", "status", "replay":
	default:
		return fmt.Errorf("action must be run, generate, send, status, or replay")
	}

	if options.delivers() {
		if options.Sender == nil {
			return fmt.Errorf("sender is required for delivery")
		}

		if err := options.Sender.validate(); err != nil {
			return err
		}
	}

	return nil
}

func (options Options) generates() bool {
	return options.Action == "run" || options.Action == "generate"
}

func (options Options) delivers() bool {
	return options.Action == "run" || options.Action == "send" || options.Action == "replay"
}

// Atomic replacements allow status readers to inspect a complete snapshot even
// while the writer is retrying an unavailable billing service.
func readRunStatus(options Options) error {
	state, err := (&Store{path: options.StatePath}).Load()
	if err != nil {
		return fmt.Errorf("read saved run: %w", err)
	}

	printStatus(options.Output, state)

	return nil
}

func resumeRun(store *Store, options Options) (*State, error) {
	state, err := store.Load()
	if os.IsNotExist(err) && options.generates() && options.Plan != nil {
		state = &State{Version: 1, Plan: *options.Plan, Delivered: make([]bool, len(options.Plan.events()))}
		err = store.Save(state)
		if err == nil {
			fmt.Fprintf(options.Output, "Prepared scenario %q with stable identities in %s\n", state.Plan.Name, options.StatePath)
		}
	}

	if err != nil {
		return nil, fmt.Errorf("open saved run: %w", err)
	}

	return state, nil
}

func releaseScenario(ctx context.Context, store *Store, state *State, options Options) error {
	if options.Plan == nil || !sameJSON(state.Plan, *options.Plan) {
		return fmt.Errorf("scenario differs from the saved run; retain its state and use send to resume pending measurements")
	}

	// A retry completes released work before generating another step. Explicit
	// offline generation remains allowed to extend the durable pending buffer.
	if options.Action != "generate" && len(state.pending(false)) != 0 {
		return nil
	}

	advance := options.Advance
	for state.NextStep < len(state.Plan.Steps) {
		if err := ctx.Err(); err != nil {
			return err
		}

		step := state.Plan.Steps[state.NextStep]
		if step.Barrier != "" {
			if !advance {
				return nil
			}

			advance = false
		}

		if err := checkpointGeneratedStep(store, state); err != nil {
			return err
		}

		fmt.Fprintf(options.Output, "Generated step %q (%d measurements)\n", step.Name, len(step.Events))
		if options.Mode == "step" {
			return nil
		}
	}

	return nil
}

func checkpointGeneratedStep(store *Store, state *State) error {
	state.NextStep++
	if err := store.Save(state); err != nil {
		state.NextStep--
		return fmt.Errorf("save generated step: %w", err)
	}

	return nil
}

func printStatus(output io.Writer, state *State) {
	released := state.released()
	pending := len(state.pending(false))
	fmt.Fprintf(output, "scenario=%s steps=%d/%d generated=%d pending=%d delivered=%d attempts=%d\n",
		state.Plan.Name, state.NextStep, len(state.Plan.Steps), released, pending, released-pending, state.Attempts)
	if state.LastError != "" {
		fmt.Fprintf(output, "Last delivery error: %s\n", state.LastError)
	}

	if state.NextStep < len(state.Plan.Steps) {
		step := state.Plan.Steps[state.NextStep]
		fmt.Fprintf(output, "Next step: %s\n", step.Name)
		if step.Barrier != "" {
			fmt.Fprintf(output, "Waiting at barrier: %s\n", step.Barrier)
		}
	}
}
