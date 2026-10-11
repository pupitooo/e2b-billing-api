package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// loadConfig requires every worker setting, preserves valid overrides, and
// rejects invalid durations, business clocks, empty heartbeat paths, and unsafe health windows.
func TestLoadConfig(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "custom heartbeat")
	tests := []struct {
		name              string
		overrides         map[string]string
		emptyEnvironment  bool
		want              config
		wantError         bool
		wantErrorContains string
		wantClockTime     string
	}{
		{
			name:      "fixed business time is configured at startup",
			overrides: map[string]string{"E2B_DEMO_NOW": "2026-11-01T08:00:00+08:00"},
			want: config{pollInterval: time.Second, batchTimeout: 5 * time.Second,
				shutdownTimeout: 5 * time.Second, heartbeatMaxAge: 15 * time.Second,
				heartbeatFile: "/tmp/billing-worker-heartbeat"},
			wantError: false, wantClockTime: "2026-11-01T00:00:00Z",
		},
		{
			name:      "invalid demo clock prevents startup",
			overrides: map[string]string{"E2B_DEMO_NOW": "2026-11-01"},
			wantError: true, wantErrorContains: "E2B_DEMO_NOW",
		},

		{
			name: "all explicit overrides are retained",
			overrides: map[string]string{
				"E2B_WORKER_POLL_INTERVAL": "250ms", "E2B_WORKER_BATCH_TIMEOUT": "3s",
				"E2B_WORKER_SHUTDOWN_TIMEOUT": "2s", "E2B_WORKER_HEARTBEAT_MAX_AGE": "4s",
				"E2B_WORKER_HEARTBEAT_FILE": filename,
			},
			want: config{pollInterval: 250 * time.Millisecond, batchTimeout: 3 * time.Second,
				shutdownTimeout: 2 * time.Second, heartbeatMaxAge: 4 * time.Second, heartbeatFile: filename},
			wantError: false,
		},
		{
			name:             "empty environment is rejected",
			emptyEnvironment: true,
			wantError:        true,
		},
		{
			name:              "whitespace-only heartbeat path is rejected",
			overrides:         map[string]string{"E2B_WORKER_HEARTBEAT_FILE": " \t\n"},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_HEARTBEAT_FILE",
		},
		{
			name:              "missing E2B_WORKER_POLL_INTERVAL",
			overrides:         map[string]string{"E2B_WORKER_POLL_INTERVAL": ""},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_POLL_INTERVAL",
		},
		{
			name:              "missing E2B_WORKER_BATCH_TIMEOUT",
			overrides:         map[string]string{"E2B_WORKER_BATCH_TIMEOUT": ""},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_BATCH_TIMEOUT",
		},
		{
			name:              "missing E2B_WORKER_SHUTDOWN_TIMEOUT",
			overrides:         map[string]string{"E2B_WORKER_SHUTDOWN_TIMEOUT": ""},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_SHUTDOWN_TIMEOUT",
		},
		{
			name:              "missing E2B_WORKER_HEARTBEAT_MAX_AGE",
			overrides:         map[string]string{"E2B_WORKER_HEARTBEAT_MAX_AGE": ""},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_HEARTBEAT_MAX_AGE",
		},
		{
			name:              "missing E2B_WORKER_HEARTBEAT_FILE",
			overrides:         map[string]string{"E2B_WORKER_HEARTBEAT_FILE": ""},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_HEARTBEAT_FILE",
		},
		{
			name:              "reject E2B_WORKER_POLL_INTERVAL=invalid",
			overrides:         map[string]string{"E2B_WORKER_POLL_INTERVAL": "invalid"},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_POLL_INTERVAL",
		},
		{
			name:              "reject E2B_WORKER_POLL_INTERVAL=10",
			overrides:         map[string]string{"E2B_WORKER_POLL_INTERVAL": "10"},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_POLL_INTERVAL",
		},
		{
			name:              "reject E2B_WORKER_POLL_INTERVAL=0s",
			overrides:         map[string]string{"E2B_WORKER_POLL_INTERVAL": "0s"},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_POLL_INTERVAL",
		},
		{
			name:              "reject E2B_WORKER_POLL_INTERVAL=-1s",
			overrides:         map[string]string{"E2B_WORKER_POLL_INTERVAL": "-1s"},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_POLL_INTERVAL",
		},
		{
			name:              "reject E2B_WORKER_BATCH_TIMEOUT=invalid",
			overrides:         map[string]string{"E2B_WORKER_BATCH_TIMEOUT": "invalid"},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_BATCH_TIMEOUT",
		},
		{
			name:              "reject E2B_WORKER_BATCH_TIMEOUT=10",
			overrides:         map[string]string{"E2B_WORKER_BATCH_TIMEOUT": "10"},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_BATCH_TIMEOUT",
		},
		{
			name:              "reject E2B_WORKER_BATCH_TIMEOUT=0s",
			overrides:         map[string]string{"E2B_WORKER_BATCH_TIMEOUT": "0s"},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_BATCH_TIMEOUT",
		},
		{
			name:              "reject E2B_WORKER_BATCH_TIMEOUT=-1s",
			overrides:         map[string]string{"E2B_WORKER_BATCH_TIMEOUT": "-1s"},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_BATCH_TIMEOUT",
		},
		{
			name:              "reject E2B_WORKER_SHUTDOWN_TIMEOUT=invalid",
			overrides:         map[string]string{"E2B_WORKER_SHUTDOWN_TIMEOUT": "invalid"},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_SHUTDOWN_TIMEOUT",
		},
		{
			name:              "reject E2B_WORKER_SHUTDOWN_TIMEOUT=10",
			overrides:         map[string]string{"E2B_WORKER_SHUTDOWN_TIMEOUT": "10"},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_SHUTDOWN_TIMEOUT",
		},
		{
			name:              "reject E2B_WORKER_SHUTDOWN_TIMEOUT=0s",
			overrides:         map[string]string{"E2B_WORKER_SHUTDOWN_TIMEOUT": "0s"},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_SHUTDOWN_TIMEOUT",
		},
		{
			name:              "reject E2B_WORKER_SHUTDOWN_TIMEOUT=-1s",
			overrides:         map[string]string{"E2B_WORKER_SHUTDOWN_TIMEOUT": "-1s"},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_SHUTDOWN_TIMEOUT",
		},
		{
			name:              "reject E2B_WORKER_HEARTBEAT_MAX_AGE=invalid",
			overrides:         map[string]string{"E2B_WORKER_HEARTBEAT_MAX_AGE": "invalid"},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_HEARTBEAT_MAX_AGE",
		},
		{
			name:              "reject E2B_WORKER_HEARTBEAT_MAX_AGE=10",
			overrides:         map[string]string{"E2B_WORKER_HEARTBEAT_MAX_AGE": "10"},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_HEARTBEAT_MAX_AGE",
		},
		{
			name:              "reject E2B_WORKER_HEARTBEAT_MAX_AGE=0s",
			overrides:         map[string]string{"E2B_WORKER_HEARTBEAT_MAX_AGE": "0s"},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_HEARTBEAT_MAX_AGE",
		},
		{
			name:              "reject E2B_WORKER_HEARTBEAT_MAX_AGE=-1s",
			overrides:         map[string]string{"E2B_WORKER_HEARTBEAT_MAX_AGE": "-1s"},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_HEARTBEAT_MAX_AGE",
		},
		{
			name:              "equal to batch plus polling",
			overrides:         map[string]string{"E2B_WORKER_HEARTBEAT_MAX_AGE": "6s"},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_HEARTBEAT_MAX_AGE",
		},
		{
			name:              "shorter than batch plus polling",
			overrides:         map[string]string{"E2B_WORKER_HEARTBEAT_MAX_AGE": "5s"},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_HEARTBEAT_MAX_AGE",
		},
		{
			name:              "batch plus polling overflows",
			overrides:         map[string]string{"E2B_WORKER_BATCH_TIMEOUT": "9223372036854775807ns"},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_HEARTBEAT_MAX_AGE",
		},
		{
			name:              "polling plus batch overflows",
			overrides:         map[string]string{"E2B_WORKER_POLL_INTERVAL": "9223372036854775807ns"},
			wantError:         true,
			wantErrorContains: "E2B_WORKER_HEARTBEAT_MAX_AGE",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := configEnvironment(tt.overrides)
			if tt.emptyEnvironment {
				getenv = func(string) string { return "" }
			}

			got, err := loadConfig(getenv)
			if tt.wantError {
				if err == nil {
					t.Fatalf("loadConfig(%v) error = nil; want an error", tt.overrides)
				}

				if !strings.Contains(err.Error(), tt.wantErrorContains) {
					t.Errorf("loadConfig error = %q; want text %q", err, tt.wantErrorContains)
				}

				return
			}

			if err != nil {
				t.Fatalf("loadConfig(%v) error = %v; want nil", tt.overrides, err)
			}

			if got.now == nil {
				t.Fatal("loadConfig clock provider = nil; want a configured business clock")
			}

			if tt.wantClockTime != "" && got.now().Format(time.RFC3339Nano) != tt.wantClockTime {
				t.Errorf("loadConfig(%v) time=%s; want %s", tt.overrides, got.now(), tt.wantClockTime)
			}

			got.now = nil

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("loadConfig(%v) = %+v; want %+v", tt.overrides, got, tt.want)
			}
		})
	}
}

// run returns the declared exit status for health probes and invalid commands
// and leaves no heartbeat behind when startup cannot proceed.
func TestRun(t *testing.T) {
	t.Run("worker healthcheck command", func(t *testing.T) {
		tests := []struct {
			name         string
			data         string
			missing      bool
			wantExitCode int
		}{
			{
				name:         "healthy",
				data:         time.Now().UTC().Format(time.RFC3339Nano),
				wantExitCode: 0,
			},
			{
				name:         "stale",
				data:         time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano),
				wantExitCode: 1,
			},
			{
				name:         "future",
				data:         time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
				wantExitCode: 1,
			},
			{
				name:         "malformed",
				data:         "invalid timestamp",
				wantExitCode: 1,
			},
			{
				name:         "missing",
				missing:      true,
				wantExitCode: 1,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				filename := filepath.Join(t.TempDir(), "heartbeat")
				if !tt.missing {
					if err := os.WriteFile(filename, []byte(tt.data), 0600); err != nil {
						t.Fatalf("Write command health fixture: %v", err)
					}
				}

				getenv := configEnvironment(map[string]string{"E2B_WORKER_HEARTBEAT_FILE": filename})
				if got := run([]string{"healthcheck"}, getenv); got != tt.wantExitCode {
					t.Fatalf("Healthcheck exit code = %d, want %d", got, tt.wantExitCode)
				}
			})
		}
	})
	t.Run("worker command rejects invalid input", func(t *testing.T) {
		tests := []struct {
			name                 string
			args                 []string
			values               map[string]string
			wantExitCode         int
			wantHeartbeatMissing bool
		}{
			{
				name:                 "unknown argument",
				args:                 []string{"unknown"},
				wantExitCode:         1,
				wantHeartbeatMissing: true,
			},
			{
				name:                 "extra healthcheck argument",
				args:                 []string{"healthcheck", "extra"},
				wantExitCode:         1,
				wantHeartbeatMissing: true,
			},
			{
				name:                 "invalid configuration",
				values:               map[string]string{"E2B_WORKER_POLL_INTERVAL": "0s"},
				wantExitCode:         1,
				wantHeartbeatMissing: true,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				filename := filepath.Join(t.TempDir(), "heartbeat")
				values := map[string]string{"E2B_WORKER_HEARTBEAT_FILE": filename}
				for name, value := range tt.values {
					values[name] = value
				}

				if got := run(tt.args, configEnvironment(values)); got != tt.wantExitCode {
					t.Fatalf("Invalid command exit code = %d, want %d", got, tt.wantExitCode)
				}

				if _, err := os.Stat(filename); errors.Is(err, os.ErrNotExist) != tt.wantHeartbeatMissing {
					t.Fatalf("Invalid command published a heartbeat: %v", err)
				}
			})
		}
	})
}

// configEnvironment supplies a complete valid environment with fixture overrides so tests
// remain independent of the developer or CI process environment.
func configEnvironment(values map[string]string) func(string) string {
	settings := map[string]string{
		"E2B_WORKER_POLL_INTERVAL":     "1s",
		"E2B_WORKER_BATCH_TIMEOUT":     "5s",
		"E2B_WORKER_SHUTDOWN_TIMEOUT":  "5s",
		"E2B_WORKER_HEARTBEAT_MAX_AGE": "15s",
		"E2B_WORKER_HEARTBEAT_FILE":    "/tmp/billing-worker-heartbeat",
	}
	for name, value := range values {
		settings[name] = value
	}

	return func(name string) string { return settings[name] }
}
