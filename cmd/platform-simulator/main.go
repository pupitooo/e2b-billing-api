package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"e2b/billing-api/internal/simulator"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "Simulator: %v\n", err)
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}

		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output, errorOutput io.Writer) error {
	options, err := parseCommandOptions(args, errorOutput)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}

	if err != nil {
		return err
	}

	if options.Action == "idle" {
		fmt.Fprintln(output, "Simulator ready. Use make simulate to release or deliver measurements.")
		<-ctx.Done()

		return nil
	}

	if options.Timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}

	if strings.HasPrefix(options.Scenario, "billing-") {
		return runBillingWorkflow(ctx, options, output)
	}

	return runUsageTransport(ctx, options, output)
}

func runUsageTransport(ctx context.Context, options commandOptions, output io.Writer) error {
	var plan *simulator.Plan
	if options.Action == "run" || options.Action == "generate" {
		built, err := buildTransportPlan(options)
		if err != nil {
			return err
		}

		plan = &built
	}

	return simulator.Run(ctx, simulator.Options{
		StatePath: options.StatePath, Action: options.Action, Mode: options.Mode,
		Advance: options.Advance, Plan: plan, Sender: options.sender(output), Output: output,
	})
}

func buildTransportPlan(options commandOptions) (simulator.Plan, error) {
	switch options.Scenario {
	case "assignment", "lost-response", "duplicates":
		if options.ScenarioFile != "" {
			return simulator.Plan{}, fmt.Errorf("file requires scenario=custom")
		}

		return simulator.Assignment(options.Source, options.Sandboxes, options.Interval)
	case "custom":
		input, err := os.Open(options.ScenarioFile)
		if err != nil {
			return simulator.Plan{}, err
		}
		defer input.Close()

		return simulator.Custom(input, options.Source)
	default:
		return simulator.Plan{}, fmt.Errorf("unknown scenario %q", options.Scenario)
	}
}
