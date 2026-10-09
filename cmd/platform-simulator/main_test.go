package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSimulatorCLISeparateGenerationAndStatus runs the public argument parser
// without billing: generation persists one step and status reports pending data
// without creating another step or attempting a network request.
func TestSimulatorCLISeparateGenerationAndStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--action=generate", "--mode=step", "--state=" + path}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "generated=2 pending=2 delivered=0 attempts=0") {
		t.Fatalf("Generation output = %s", output.String())
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(context.Background(), []string{"--action=status", "--state=" + path}, &output, &output); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || !strings.Contains(output.String(), "steps=1/4") {
		t.Fatalf("Status changed generation: %s, %v", output.String(), err)
	}
}

// TestSimulatorCLIRejectsInvalidDeliveryBeforeStorage ensures invalid transport
// settings and unsupported input cannot create a misleading saved run.
func TestSimulatorCLIRejectsInvalidDeliveryBeforeStorage(t *testing.T) {
	for _, argument := range []string{"--batch-size=1001", "--api-url=file:///tmp/inbox", "--mode=unknown", "--timeout=0s", "--scenario=unknown", "--file=unrequested.json"} {
		t.Run(argument, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "run.json")
			var output bytes.Buffer
			if err := run(context.Background(), []string{"--state=" + path, argument}, &output, &output); err == nil {
				t.Error("Invalid CLI argument was accepted")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("Invalid CLI created sender state: %v", err)
			}
		})
	}
}
