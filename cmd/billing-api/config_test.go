package main

import (
	"strings"
	"testing"
	"time"
)

// loadConfig returns explicit runtime budgets for defaults and overrides, and
// rejects malformed or incompatible settings instead of silently starting.
func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name              string
		environment       map[string]string
		want              config
		wantError         bool
		wantErrorContains string
	}{
		{
			name:        "empty environment uses documented budgets",
			environment: nil,
			want: config{
				startupTimeout: 10 * time.Second, readHeaderTimeout: 5 * time.Second,
				readTimeout: 15 * time.Second, writeTimeout: 35 * time.Second,
				idleTimeout: 90 * time.Second, ingestionTimeout: 10 * time.Second,
				rollbackTimeout: 5 * time.Second, shutdownTimeout: 45 * time.Second,
				poolMaxConns: 8, poolMinConns: 2, maxInFlight: 32,
			},
			wantError: false,
		},
		{
			name: "explicit overrides include zero minimum connections and fractional durations",
			environment: map[string]string{
				"E2B_API_STARTUP_TIMEOUT": "20s", "E2B_API_READ_HEADER_TIMEOUT": "2s",
				"E2B_API_READ_TIMEOUT": "10s", "E2B_API_WRITE_TIMEOUT": "25s",
				"E2B_API_IDLE_TIMEOUT": "2m", "E2B_API_INGESTION_TIMEOUT": "4000ms",
				"E2B_API_ROLLBACK_TIMEOUT": "750ms", "E2B_API_SHUTDOWN_TIMEOUT": "35s",
				"E2B_API_DB_MAX_CONNS": "16", "E2B_API_DB_MIN_CONNS": "0",
				"E2B_API_MAX_IN_FLIGHT_BATCHES": "64",
			},
			want: config{
				startupTimeout: 20 * time.Second, readHeaderTimeout: 2 * time.Second,
				readTimeout: 10 * time.Second, writeTimeout: 25 * time.Second,
				idleTimeout: 2 * time.Minute, ingestionTimeout: 4 * time.Second,
				rollbackTimeout: 750 * time.Millisecond, shutdownTimeout: 35 * time.Second,
				poolMaxConns: 16, poolMinConns: 0, maxInFlight: 64,
			},
			wantError: false,
		},
		{
			name:              "reject E2B_API_STARTUP_TIMEOUT=10",
			environment:       map[string]string{"E2B_API_STARTUP_TIMEOUT": "10"},
			wantError:         true,
			wantErrorContains: "E2B_API_",
		},
		{
			name:              "reject E2B_API_READ_HEADER_TIMEOUT=0s",
			environment:       map[string]string{"E2B_API_READ_HEADER_TIMEOUT": "0s"},
			wantError:         true,
			wantErrorContains: "E2B_API_",
		},
		{
			name:              "reject E2B_API_IDLE_TIMEOUT=-1s",
			environment:       map[string]string{"E2B_API_IDLE_TIMEOUT": "-1s"},
			wantError:         true,
			wantErrorContains: "E2B_API_",
		},
		{
			name:              "reject E2B_API_INGESTION_TIMEOUT=100000000000h",
			environment:       map[string]string{"E2B_API_INGESTION_TIMEOUT": "100000000000h"},
			wantError:         true,
			wantErrorContains: "E2B_API_",
		},
		{
			name:              "reject E2B_API_ROLLBACK_TIMEOUT=0s",
			environment:       map[string]string{"E2B_API_ROLLBACK_TIMEOUT": "0s"},
			wantError:         true,
			wantErrorContains: "E2B_API_",
		},
		{
			name:              "reject E2B_API_ROLLBACK_TIMEOUT=-1s",
			environment:       map[string]string{"E2B_API_ROLLBACK_TIMEOUT": "-1s"},
			wantError:         true,
			wantErrorContains: "E2B_API_",
		},
		{
			name:              "reject E2B_API_ROLLBACK_TIMEOUT=bad",
			environment:       map[string]string{"E2B_API_ROLLBACK_TIMEOUT": "bad"},
			wantError:         true,
			wantErrorContains: "E2B_API_",
		},
		{
			name:              "reject E2B_API_ROLLBACK_TIMEOUT=10s",
			environment:       map[string]string{"E2B_API_ROLLBACK_TIMEOUT": "10s"},
			wantError:         true,
			wantErrorContains: "E2B_API_",
		},
		{
			name:              "reject E2B_API_DB_MAX_CONNS=0",
			environment:       map[string]string{"E2B_API_DB_MAX_CONNS": "0"},
			wantError:         true,
			wantErrorContains: "E2B_API_",
		},
		{
			name:              "reject E2B_API_DB_MIN_CONNS=-1",
			environment:       map[string]string{"E2B_API_DB_MIN_CONNS": "-1"},
			wantError:         true,
			wantErrorContains: "E2B_API_",
		},
		{
			name:              "reject E2B_API_DB_MIN_CONNS=9",
			environment:       map[string]string{"E2B_API_DB_MIN_CONNS": "9"},
			wantError:         true,
			wantErrorContains: "E2B_API_",
		},
		{
			name:              "reject E2B_API_DB_MAX_CONNS=2147483648",
			environment:       map[string]string{"E2B_API_DB_MAX_CONNS": "2147483648"},
			wantError:         true,
			wantErrorContains: "E2B_API_",
		},
		{
			name:              "reject E2B_API_DB_MAX_CONNS=1.5",
			environment:       map[string]string{"E2B_API_DB_MAX_CONNS": "1.5"},
			wantError:         true,
			wantErrorContains: "E2B_API_",
		},
		{
			name:              "reject E2B_API_MAX_IN_FLIGHT_BATCHES=0",
			environment:       map[string]string{"E2B_API_MAX_IN_FLIGHT_BATCHES": "0"},
			wantError:         true,
			wantErrorContains: "E2B_API_",
		},
		{
			name:              "reject E2B_API_READ_HEADER_TIMEOUT=20s",
			environment:       map[string]string{"E2B_API_READ_HEADER_TIMEOUT": "20s"},
			wantError:         true,
			wantErrorContains: "E2B_API_",
		},
		{
			name:              "reject E2B_API_WRITE_TIMEOUT=30s",
			environment:       map[string]string{"E2B_API_WRITE_TIMEOUT": "30s"},
			wantError:         true,
			wantErrorContains: "E2B_API_",
		},
		{
			name:              "reject E2B_API_SHUTDOWN_TIMEOUT=40s",
			environment:       map[string]string{"E2B_API_SHUTDOWN_TIMEOUT": "40s"},
			wantError:         true,
			wantErrorContains: "E2B_API_",
		},
		{
			name: "combined duration overflow is rejected",
			environment: map[string]string{
				"E2B_API_READ_TIMEOUT": "2000000000s", "E2B_API_INGESTION_TIMEOUT": "2000000000s",
				"E2B_API_WRITE_TIMEOUT": "3000000000s", "E2B_API_SHUTDOWN_TIMEOUT": "4000000000s",
			},
			wantError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := loadConfig(configEnvironment(tt.environment))
			if tt.wantError {
				if err == nil {
					t.Fatalf("loadConfig(%v) error = nil; want an error", tt.environment)
				}
				if !strings.Contains(err.Error(), tt.wantErrorContains) {
					t.Errorf("loadConfig error = %q; want text %q", err, tt.wantErrorContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadConfig(%v) error = %v; want nil", tt.environment, err)
			}
			if got != tt.want {
				t.Errorf("loadConfig(%v) = %+v; want %+v", tt.environment, got, tt.want)
			}
		})
	}
}

// Supply exactly the declared environment without inheriting local process settings.
func configEnvironment(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}
