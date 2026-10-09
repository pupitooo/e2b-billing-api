package main

import (
	"fmt"
	"strconv"
	"time"
)

type config struct {
	startupTimeout    time.Duration
	readHeaderTimeout time.Duration
	readTimeout       time.Duration
	writeTimeout      time.Duration
	idleTimeout       time.Duration
	ingestionTimeout  time.Duration
	rollbackTimeout   time.Duration
	shutdownTimeout   time.Duration
	poolMaxConns      int32
	poolMinConns      int32
	maxInFlight       int32
}

// loadConfig reads operational settings once before opening resources.
// Defaults allow minute-based reporting and bound the API's database budget;
// workload assumptions and measurement limits are recorded in the README.
func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		startupTimeout: 10 * time.Second, readHeaderTimeout: 5 * time.Second,
		readTimeout: 15 * time.Second, writeTimeout: 35 * time.Second,
		idleTimeout: 90 * time.Second, ingestionTimeout: 10 * time.Second,
		rollbackTimeout: 5 * time.Second,
		shutdownTimeout: 45 * time.Second, poolMaxConns: 8, poolMinConns: 2,
		maxInFlight: 32,
	}
	for _, setting := range []struct {
		name  string
		value *time.Duration
	}{
		{"E2B_API_STARTUP_TIMEOUT", &cfg.startupTimeout},
		{"E2B_API_READ_HEADER_TIMEOUT", &cfg.readHeaderTimeout},
		{"E2B_API_READ_TIMEOUT", &cfg.readTimeout},
		{"E2B_API_WRITE_TIMEOUT", &cfg.writeTimeout},
		{"E2B_API_IDLE_TIMEOUT", &cfg.idleTimeout},
		{"E2B_API_INGESTION_TIMEOUT", &cfg.ingestionTimeout},
		{"E2B_API_ROLLBACK_TIMEOUT", &cfg.rollbackTimeout},
		{"E2B_API_SHUTDOWN_TIMEOUT", &cfg.shutdownTimeout},
	} {
		if value := getenv(setting.name); value != "" {
			duration, err := time.ParseDuration(value)
			if err != nil || duration <= 0 {
				return config{}, fmt.Errorf("%s must be a positive Go duration", setting.name)
			}
			*setting.value = duration
		}
	}
	for _, setting := range []struct {
		name  string
		value *int32
	}{
		{"E2B_API_DB_MAX_CONNS", &cfg.poolMaxConns},
		{"E2B_API_DB_MIN_CONNS", &cfg.poolMinConns},
		{"E2B_API_MAX_IN_FLIGHT_BATCHES", &cfg.maxInFlight},
	} {
		if value := getenv(setting.name); value != "" {
			count, err := strconv.ParseInt(value, 10, 32)
			if err != nil || count < 0 {
				return config{}, fmt.Errorf("%s must be a nonnegative int32", setting.name)
			}
			*setting.value = int32(count)
		}
	}
	if cfg.poolMaxConns == 0 || cfg.poolMinConns > cfg.poolMaxConns {
		return config{}, fmt.Errorf("E2B_API_DB_MAX_CONNS must be positive and at least E2B_API_DB_MIN_CONNS")
	}
	if cfg.maxInFlight == 0 {
		return config{}, fmt.Errorf("E2B_API_MAX_IN_FLIGHT_BATCHES must be positive")
	}
	if cfg.readHeaderTimeout > cfg.readTimeout {
		return config{}, fmt.Errorf("E2B_API_READ_HEADER_TIMEOUT must not exceed E2B_API_READ_TIMEOUT")
	}
	// Subtraction avoids overflow for otherwise valid, extremely large durations.
	// A slow body and database cancellation must leave time for rollback and a reply.
	if cfg.readTimeout >= cfg.writeTimeout ||
		cfg.ingestionTimeout >= cfg.writeTimeout-cfg.readTimeout ||
		cfg.rollbackTimeout >= cfg.writeTimeout-cfg.readTimeout-cfg.ingestionTimeout {
		return config{}, fmt.Errorf("E2B_API_WRITE_TIMEOUT must exceed the read timeout, ingestion timeout, and %s rollback budget combined", cfg.rollbackTimeout)
	}
	if cfg.readHeaderTimeout >= cfg.shutdownTimeout ||
		cfg.writeTimeout >= cfg.shutdownTimeout-cfg.readHeaderTimeout {
		return config{}, fmt.Errorf("E2B_API_SHUTDOWN_TIMEOUT must exceed the header and write timeouts combined")
	}
	return cfg, nil
}
