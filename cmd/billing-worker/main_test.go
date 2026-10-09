package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLoadConfigRequiresEnvironment verifies that each missing or empty setting
// fails with its name instead of silently selecting a runtime default.
func TestLoadConfigRequiresEnvironment(t *testing.T) {
	for _, name := range []string{
		"E2B_WORKER_POLL_INTERVAL", "E2B_WORKER_BATCH_TIMEOUT",
		"E2B_WORKER_SHUTDOWN_TIMEOUT", "E2B_WORKER_HEARTBEAT_MAX_AGE",
		"E2B_WORKER_HEARTBEAT_FILE",
	} {
		t.Run(name, func(t *testing.T) {
			getenv := configEnvironment(nil)
			_, err := loadConfig(func(key string) string {
				if key == name {
					return ""
				}
				return getenv(key)
			})
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("Missing-setting error = %v, want %s", err, name)
			}
		})
	}
	if _, err := loadConfig(func(string) string { return "" }); err == nil {
		t.Fatal("An empty environment must fail configuration")
	}
	if _, err := loadConfig(configEnvironment(map[string]string{"E2B_WORKER_HEARTBEAT_FILE": " \t\n"})); err == nil {
		t.Fatal("A whitespace-only heartbeat path must fail configuration")
	}
}

// TestLoadConfigOverrides verifies that all supported environment settings
// supply the runtime configuration, including fractional intervals and a custom file.
func TestLoadConfigOverrides(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "custom heartbeat")
	values := map[string]string{
		"E2B_WORKER_POLL_INTERVAL":     "250ms",
		"E2B_WORKER_BATCH_TIMEOUT":     "3s",
		"E2B_WORKER_SHUTDOWN_TIMEOUT":  "2s",
		"E2B_WORKER_HEARTBEAT_MAX_AGE": "4s",
		"E2B_WORKER_HEARTBEAT_FILE":    filename,
	}
	cfg, err := loadConfig(configEnvironment(values))
	if err != nil {
		t.Fatalf("Load overridden configuration: %v", err)
	}
	want := config{
		pollInterval: 250 * time.Millisecond, batchTimeout: 3 * time.Second,
		shutdownTimeout: 2 * time.Second, heartbeatMaxAge: 4 * time.Second,
		heartbeatFile: filename,
	}
	if cfg != want {
		t.Fatalf("Overridden configuration = %+v, want %+v", cfg, want)
	}
}

// TestLoadConfigRejectsInvalidDurations verifies that every duration setting
// rejects malformed, unitless, zero, and negative values with its setting name.
func TestLoadConfigRejectsInvalidDurations(t *testing.T) {
	for _, setting := range []string{
		"E2B_WORKER_POLL_INTERVAL", "E2B_WORKER_BATCH_TIMEOUT",
		"E2B_WORKER_SHUTDOWN_TIMEOUT", "E2B_WORKER_HEARTBEAT_MAX_AGE",
	} {
		for _, value := range []string{"invalid", "10", "0s", "-1s"} {
			t.Run(setting+"="+value, func(t *testing.T) {
				_, err := loadConfig(configEnvironment(map[string]string{setting: value}))
				if err == nil || !strings.Contains(err.Error(), setting) {
					t.Fatalf("Configuration error = %v, want an error identifying %s", err, setting)
				}
			})
		}
	}
}

// TestLoadConfigHeartbeatWindow verifies that health cannot become stale during
// a permitted batch plus idle wait, including when duration addition overflows.
func TestLoadConfigHeartbeatWindow(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
	}{
		{name: "equal to batch plus polling", values: map[string]string{"E2B_WORKER_HEARTBEAT_MAX_AGE": "6s"}},
		{name: "shorter than batch plus polling", values: map[string]string{"E2B_WORKER_HEARTBEAT_MAX_AGE": "5s"}},
		{name: "batch plus polling overflows", values: map[string]string{"E2B_WORKER_BATCH_TIMEOUT": "9223372036854775807ns"}},
		{name: "polling plus batch overflows", values: map[string]string{"E2B_WORKER_POLL_INTERVAL": "9223372036854775807ns"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadConfig(configEnvironment(tt.values))
			if err == nil || !strings.Contains(err.Error(), "E2B_WORKER_HEARTBEAT_MAX_AGE") {
				t.Fatalf("Heartbeat-window error = %v, want rejection naming E2B_WORKER_HEARTBEAT_MAX_AGE", err)
			}
		})
	}
}

// TestWorkerHealthcheckCommand verifies that the executable's healthcheck mode
// exits successfully only for a readable, fresh, valid heartbeat.
func TestWorkerHealthcheckCommand(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		missing bool
		want    int
	}{
		{name: "healthy", data: time.Now().UTC().Format(time.RFC3339Nano)},
		{name: "stale", data: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), want: 1},
		{name: "future", data: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), want: 1},
		{name: "malformed", data: "invalid timestamp", want: 1},
		{name: "missing", missing: true, want: 1},
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
			if got := run([]string{"healthcheck"}, getenv); got != tt.want {
				t.Fatalf("Healthcheck exit code = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestWorkerCommandRejectsInvalidInput verifies that invalid arguments and
// configuration fail immediately without starting a worker or creating health.
func TestWorkerCommandRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		values map[string]string
	}{
		{name: "unknown argument", args: []string{"unknown"}},
		{name: "extra healthcheck argument", args: []string{"healthcheck", "extra"}},
		{name: "invalid configuration", values: map[string]string{"E2B_WORKER_POLL_INTERVAL": "0s"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "heartbeat")
			values := map[string]string{"E2B_WORKER_HEARTBEAT_FILE": filename}
			for name, value := range tt.values {
				values[name] = value
			}
			if got := run(tt.args, configEnvironment(values)); got != 1 {
				t.Fatalf("Invalid command exit code = %d, want 1", got)
			}
			if _, err := os.Stat(filename); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Invalid command published a heartbeat: %v", err)
			}
		})
	}
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
