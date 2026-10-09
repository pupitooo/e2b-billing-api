package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	flags := flag.NewFlagSet("platform-simulator", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	action := flags.String("action", "run", "run, generate, send, status, replay, or idle")
	scenario := flags.String("scenario", "assignment", "assignment, lost-response, duplicates, or custom")
	mode := flags.String("mode", "fast", "fast releases a phase; step releases one step per command")
	state := flags.String("state", "/state/run.json", "durable sender state file; retain it across restarts")
	source := flags.String("source", "platform-simulator", "stable producer namespace for the saved run")
	file := flags.String("file", "", "custom scenario JSON path")
	sandboxes := flags.Int("sandboxes", 1, "sandbox count per customer in the assignment")
	interval := flags.Duration("interval", time.Hour, "assignment measurement interval, dividing 1h (1m to 1h)")
	advance := flags.Bool("advance", false, "explicitly release the next operator barrier")
	baseURL := flags.String("api-url", "http://api:8080", "billing HTTP base URL")
	batchSize := flags.Int("batch-size", 100, "maximum events per request (1 to 1000)")
	batchDelay := flags.Duration("delay", 0, "delay between successful batches")
	timeout := flags.Duration("timeout", 15*time.Second, "timeout of each HTTP attempt")
	retryMin := flags.Duration("retry-min", time.Second, "initial retry delay")
	retryMax := flags.Duration("retry-max", 30*time.Second, "maximum retry backoff; Retry-After is a minimum")
	maxAttempts := flags.Int("max-attempts", 0, "attempt limit per batch; zero retries until interrupted")
	duplicates := flags.Int("duplicates", 0, "extra identical copies per batch (0 to 10)")
	loseResponse := flags.Bool("lose-response", false, "ignore the first successful response once per saved run")
	reverse := flags.Bool("reverse", false, "send released events in reverse order")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if *action == "idle" {
		fmt.Fprintln(output, "Simulator ready. Use make simulate to release or deliver measurements.")
		<-ctx.Done()
		return nil
	}
	if *timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	var plan *simulator.Plan
	if *action == "run" || *action == "generate" {
		var built simulator.Plan
		var err error
		switch *scenario {
		case "assignment", "lost-response", "duplicates":
			if *file != "" {
				return fmt.Errorf("file requires scenario=custom")
			}
			built, err = simulator.Assignment(*source, *sandboxes, *interval)
		case "custom":
			var input *os.File
			input, err = os.Open(*file)
			if err == nil {
				defer input.Close()
				built, err = simulator.Custom(input, *source)
			}
		default:
			return fmt.Errorf("unknown scenario %q", *scenario)
		}
		if err != nil {
			return err
		}
		plan = &built
	}
	if *scenario == "lost-response" {
		*loseResponse = true
	}
	if *scenario == "duplicates" && *duplicates == 0 {
		*duplicates = 1
	}
	sender := &simulator.Sender{
		BaseURL: *baseURL, Client: &http.Client{Timeout: *timeout},
		BatchSize: *batchSize, BatchDelay: *batchDelay, RetryMin: *retryMin, RetryMax: *retryMax,
		MaxAttempts: *maxAttempts, Duplicates: *duplicates, LoseResponse: *loseResponse,
		Reverse: *reverse, Output: output,
	}
	return simulator.Run(ctx, simulator.Options{
		StatePath: *state, Action: *action, Mode: *mode, Advance: *advance,
		Plan: plan, Sender: sender, Output: output,
	})
}
