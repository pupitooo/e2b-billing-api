package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"e2b/billing-api/internal/simulator"
)

type commandOptions struct {
	Action, Scenario, Mode, StatePath, Source, ScenarioFile, BaseURL string
	Sandboxes, BatchSize, MaxAttempts, Duplicates                    int
	Interval, BatchDelay, Timeout, RetryMin, RetryMax                time.Duration
	Advance, LoseResponse, Reverse                                   bool
}

func parseCommandOptions(args []string, errorOutput io.Writer) (commandOptions, error) {
	var options commandOptions
	flags := flag.NewFlagSet("platform-simulator", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	flags.StringVar(&options.Action, "action", "run", "run, generate, send, status, replay, or idle")
	flags.StringVar(&options.Scenario, "scenario", "assignment", "transport assignment, lost-response, duplicates, custom, or a billing-* public workflow")
	flags.StringVar(&options.Mode, "mode", "fast", "fast releases a phase; step releases one step per command")
	flags.StringVar(&options.StatePath, "state", "/state/run.json", "durable sender state file; retain it across restarts")
	flags.StringVar(&options.Source, "source", "platform-simulator", "stable producer namespace for the saved run")
	flags.StringVar(&options.ScenarioFile, "file", "", "custom scenario JSON path")
	flags.IntVar(&options.Sandboxes, "sandboxes", 1, "sandbox count per customer in the assignment")
	flags.DurationVar(&options.Interval, "interval", time.Hour, "assignment measurement interval, dividing 1h (1m to 1h)")
	flags.BoolVar(&options.Advance, "advance", false, "explicitly release the next operator barrier")
	flags.StringVar(&options.BaseURL, "api-url", "http://api:8080", "billing HTTP base URL")
	flags.IntVar(&options.BatchSize, "batch-size", 100, "maximum events per request (1 to 1000)")
	flags.DurationVar(&options.BatchDelay, "delay", 0, "delay between successful batches")
	flags.DurationVar(&options.Timeout, "timeout", 15*time.Second, "timeout of each HTTP attempt")
	flags.DurationVar(&options.RetryMin, "retry-min", time.Second, "initial retry delay")
	flags.DurationVar(&options.RetryMax, "retry-max", 30*time.Second, "maximum retry backoff; Retry-After is a minimum")
	flags.IntVar(&options.MaxAttempts, "max-attempts", 0, "attempt limit per batch; zero retries until interrupted")
	flags.IntVar(&options.Duplicates, "duplicates", 0, "extra identical copies per batch (0 to 10)")
	flags.BoolVar(&options.LoseResponse, "lose-response", false, "ignore the first successful response once per saved run")
	flags.BoolVar(&options.Reverse, "reverse", false, "send released events in reverse order")
	if err := flags.Parse(args); err != nil {
		return options, err
	}

	if flags.NArg() != 0 {
		return options, fmt.Errorf("unexpected positional arguments")
	}

	return options, nil
}

func (options commandOptions) sender(output io.Writer) *simulator.Sender {
	duplicates := options.Duplicates
	if options.Scenario == "duplicates" && duplicates == 0 {
		duplicates = 1
	}

	return &simulator.Sender{
		BaseURL: options.BaseURL, Client: &http.Client{Timeout: options.Timeout},
		BatchSize: options.BatchSize, BatchDelay: options.BatchDelay, RetryMin: options.RetryMin, RetryMax: options.RetryMax,
		MaxAttempts: options.MaxAttempts, Duplicates: duplicates,
		LoseResponse: options.LoseResponse || options.Scenario == "lost-response", Reverse: options.Reverse, Output: output,
	}
}

func runBillingWorkflow(ctx context.Context, options commandOptions, output io.Writer) error {
	if options.changesTransportInputs() {
		return fmt.Errorf("billing workflows declare their inputs; transport generation flags require a transport scenario")
	}

	path := options.ScenarioFile
	if path == "" {
		path = filepath.Join("docs/simulator", options.Scenario+".json")
	}

	plan, err := simulator.LoadWorkflow(path)
	if err != nil {
		return err
	}

	return simulator.RunWorkflow(ctx, simulator.WorkflowOptions{
		Plan: plan, StatePath: options.StatePath, Source: options.Source, BaseURL: options.BaseURL,
		Client: &http.Client{Timeout: options.Timeout}, Action: options.Action, Mode: options.Mode,
		RetryMin: options.RetryMin, RetryMax: options.RetryMax, MaxAttempts: options.MaxAttempts,
		LoseResponse: options.LoseResponse, Output: output,
	})
}

func (options commandOptions) changesTransportInputs() bool {
	return options.Sandboxes != 1 || options.Interval != time.Hour || options.Advance ||
		options.BatchSize != 100 || options.BatchDelay != 0 || options.Duplicates != 0 || options.Reverse
}
