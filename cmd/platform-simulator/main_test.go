package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// run persists explicit generation steps, reports status without advancing,
// and rejects invalid CLI input before creating a saved run.
func TestRun(t *testing.T) {
	t.Run("simulator cliseparate generation and status", func(t *testing.T) {
		tt := struct {
			generateArgs         []string
			statusArgs           []string
			wantGenerationOutput string
			wantStatusOutput     string
			wantStateUnchanged   bool
		}{
			generateArgs:         []string{"--action=generate", "--mode=step"},
			statusArgs:           []string{"--action=status"},
			wantGenerationOutput: "generated=2 pending=2 delivered=0 attempts=0",
			wantStatusOutput:     "steps=1/4",
			wantStateUnchanged:   true,
		}

		path := filepath.Join(t.TempDir(), "run.json")
		var output bytes.Buffer
		if err := run(context.Background(), append(tt.generateArgs, "--state="+path), &output, &output); err != nil {
			t.Fatal(err)
		}

		if !strings.Contains(output.String(), tt.wantGenerationOutput) {
			t.Fatalf("Generation output = %s", output.String())
		}

		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		output.Reset()
		if err := run(context.Background(), append(tt.statusArgs, "--state="+path), &output, &output); err != nil {
			t.Fatal(err)
		}

		after, err := os.ReadFile(path)
		if err != nil || bytes.Equal(before, after) != tt.wantStateUnchanged || !strings.Contains(output.String(), tt.wantStatusOutput) {
			t.Fatalf("Status changed generation: %s, %v", output.String(), err)
		}
	})
	t.Run("simulator clirejects invalid delivery before storage", func(t *testing.T) {
		tests := []struct {
			name             string
			argument         string
			wantError        bool
			wantStateMissing bool
		}{
			{
				name:             "--batch-size=1001",
				argument:         "--batch-size=1001",
				wantError:        true,
				wantStateMissing: true,
			},
			{
				name:             "--api-url=file:///tmp/inbox",
				argument:         "--api-url=file:///tmp/inbox",
				wantError:        true,
				wantStateMissing: true,
			},
			{
				name:             "--mode=unknown",
				argument:         "--mode=unknown",
				wantError:        true,
				wantStateMissing: true,
			},
			{
				name:             "--timeout=0s",
				argument:         "--timeout=0s",
				wantError:        true,
				wantStateMissing: true,
			},
			{
				name:             "--scenario=unknown",
				argument:         "--scenario=unknown",
				wantError:        true,
				wantStateMissing: true,
			},
			{
				name:             "--file=unrequested.json",
				argument:         "--file=unrequested.json",
				wantError:        true,
				wantStateMissing: true,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "run.json")
				var output bytes.Buffer
				if err := run(context.Background(), []string{"--state=" + path, tt.argument}, &output, &output); (err != nil) != tt.wantError {
					t.Error("Invalid CLI argument was accepted")
				}

				if _, err := os.Stat(path); os.IsNotExist(err) != tt.wantStateMissing {
					t.Errorf("Invalid CLI created sender state: %v", err)
				}
			})
		}
	})
}
