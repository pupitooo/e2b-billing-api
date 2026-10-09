package main

import (
	"strings"
	"testing"
	"time"
)

// TestLoadConfigDefaults checks the documented per-process budgets remain
// available with no environment settings, including the five-second rollback fallback.
func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(configEnvironment(nil))
	if err != nil {
		t.Fatalf("Load default configuration: %v", err)
	}
	if cfg.poolMaxConns != 8 || cfg.poolMinConns != 2 || cfg.maxInFlight != 32 ||
		cfg.idleTimeout != 90*time.Second || cfg.ingestionTimeout != 10*time.Second ||
		cfg.rollbackTimeout != 5*time.Second {
		t.Fatalf("Unexpected default budgets: %+v", cfg)
	}
}

// TestLoadConfigOverrides verifies environment durations retain explicit units,
// zero minimum connections is supported, and every operational setting changes
// independently of rebuilding the binary, including the rollback budget.
func TestLoadConfigOverrides(t *testing.T) {
	cfg, err := loadConfig(configEnvironment(map[string]string{
		"E2B_API_STARTUP_TIMEOUT": "20s", "E2B_API_READ_HEADER_TIMEOUT": "2s",
		"E2B_API_READ_TIMEOUT": "10s", "E2B_API_WRITE_TIMEOUT": "25s",
		"E2B_API_IDLE_TIMEOUT": "2m", "E2B_API_INGESTION_TIMEOUT": "4000ms",
		"E2B_API_ROLLBACK_TIMEOUT": "750ms",
		"E2B_API_SHUTDOWN_TIMEOUT": "35s", "E2B_API_DB_MAX_CONNS": "16",
		"E2B_API_DB_MIN_CONNS": "0", "E2B_API_MAX_IN_FLIGHT_BATCHES": "64",
	}))
	if err != nil {
		t.Fatalf("Load overridden configuration: %v", err)
	}
	expected := config{
		startupTimeout: 20 * time.Second, readHeaderTimeout: 2 * time.Second,
		readTimeout: 10 * time.Second, writeTimeout: 25 * time.Second,
		idleTimeout: 2 * time.Minute, ingestionTimeout: 4 * time.Second,
		rollbackTimeout: 750 * time.Millisecond,
		shutdownTimeout: 35 * time.Second, poolMaxConns: 16, poolMinConns: 0,
		maxInFlight: 64,
	}
	if cfg != expected {
		t.Errorf("Configuration = %+v, want %+v", cfg, expected)
	}
}

// TestLoadConfigRejectsInvalidSettings requires startup failure for malformed,
// disabled, overflowing, or incompatible budgets instead of silently falling
// back and starting with a different configuration from the operator's input.
// Rollback overrides must also fit the combined HTTP response budget.
func TestLoadConfigRejectsInvalidSettings(t *testing.T) {
	for _, tt := range []struct{ name, value string }{
		{"E2B_API_STARTUP_TIMEOUT", "10"},
		{"E2B_API_READ_HEADER_TIMEOUT", "0s"},
		{"E2B_API_IDLE_TIMEOUT", "-1s"},
		{"E2B_API_INGESTION_TIMEOUT", "100000000000h"},
		{"E2B_API_ROLLBACK_TIMEOUT", "0s"},
		{"E2B_API_ROLLBACK_TIMEOUT", "-1s"},
		{"E2B_API_ROLLBACK_TIMEOUT", "bad"},
		{"E2B_API_ROLLBACK_TIMEOUT", "10s"},
		{"E2B_API_DB_MAX_CONNS", "0"},
		{"E2B_API_DB_MIN_CONNS", "-1"},
		{"E2B_API_DB_MIN_CONNS", "9"},
		{"E2B_API_DB_MAX_CONNS", "2147483648"},
		{"E2B_API_DB_MAX_CONNS", "1.5"},
		{"E2B_API_MAX_IN_FLIGHT_BATCHES", "0"},
		{"E2B_API_READ_HEADER_TIMEOUT", "20s"},
		{"E2B_API_WRITE_TIMEOUT", "30s"},
		{"E2B_API_SHUTDOWN_TIMEOUT", "40s"},
	} {
		t.Run(tt.name+"="+tt.value, func(t *testing.T) {
			_, err := loadConfig(configEnvironment(map[string]string{tt.name: tt.value}))
			if err == nil || !strings.Contains(err.Error(), "E2B_API_") {
				t.Fatalf("Invalid setting error = %v", err)
			}
		})
	}
	_, err := loadConfig(configEnvironment(map[string]string{
		"E2B_API_READ_TIMEOUT": "2000000000s", "E2B_API_INGESTION_TIMEOUT": "2000000000s",
		"E2B_API_WRITE_TIMEOUT": "3000000000s", "E2B_API_SHUTDOWN_TIMEOUT": "4000000000s",
	}))
	if err == nil {
		t.Fatal("Oversized combined budgets were accepted")
	}
}

// configEnvironment supplies deterministic settings without mutating process
// environment variables or inheriting the developer's local configuration.
func configEnvironment(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}
