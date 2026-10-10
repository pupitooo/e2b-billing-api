package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/worker"
)

const workerPoolMaxConns = 2

type config struct {
	pollInterval    time.Duration
	batchTimeout    time.Duration
	shutdownTimeout time.Duration
	heartbeatMaxAge time.Duration
	heartbeatFile   string
}

func main() {
	os.Exit(run(os.Args[1:], os.Getenv))
}

func run(args []string, getenv func(string) string) int {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := loadConfig(getenv)
	if err != nil {
		logger.Error("Invalid worker configuration", "error", err)
		return 1
	}

	if len(args) == 1 && args[0] == "healthcheck" {
		if err := worker.CheckHealth(cfg.heartbeatFile, cfg.heartbeatMaxAge, time.Now); err != nil {
			logger.Error("Worker health check failed", "error", err)
			return 1
		}

		return 0
	}

	if len(args) != 0 {
		logger.Error("Usage: billing-worker [healthcheck]")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, cfg.batchTimeout)
	pool, err := inbox.OpenPool(startup, getenv("DATABASE_URL"), inbox.PoolLimits{MaxConns: workerPoolMaxConns, MinConns: 0})
	cancel()
	if err != nil {
		logger.Error("Initialize accounting database", "error", err)
		return 1
	}

	defer pool.Close()
	logger.Info("Starting standalone billing worker", "accounting_enabled", true,
		"poll_interval", cfg.pollInterval.String(), "batch_timeout", cfg.batchTimeout.String())
	w := worker.Worker{
		ProcessBatch:  billing.NewStore(pool).ProcessBatch,
		PollInterval:  cfg.pollInterval,
		BatchTimeout:  cfg.batchTimeout,
		HeartbeatFile: cfg.heartbeatFile,
		Logger:        logger,
	}
	if err := worker.RunUntilStopped(ctx, w, cfg.shutdownTimeout); err != nil {
		logger.Error("Billing worker stopped with an error", "error", err)
		return 1
	}

	logger.Info("Billing worker stopped")

	return 0
}

func loadConfig(getenv func(string) string) (config, error) {
	var cfg config
	settings := []struct {
		name  string
		value *time.Duration
	}{
		{"E2B_WORKER_POLL_INTERVAL", &cfg.pollInterval},
		{"E2B_WORKER_BATCH_TIMEOUT", &cfg.batchTimeout},
		{"E2B_WORKER_SHUTDOWN_TIMEOUT", &cfg.shutdownTimeout},
		{"E2B_WORKER_HEARTBEAT_MAX_AGE", &cfg.heartbeatMaxAge},
	}
	for _, setting := range settings {
		value := getenv(setting.name)
		if value == "" {
			return config{}, fmt.Errorf("%s is required", setting.name)
		}

		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 {
			return config{}, fmt.Errorf("%s must be a positive Go duration", setting.name)
		}

		*setting.value = duration
	}

	cfg.heartbeatFile = getenv("E2B_WORKER_HEARTBEAT_FILE")
	if strings.TrimSpace(cfg.heartbeatFile) == "" {
		return config{}, fmt.Errorf("E2B_WORKER_HEARTBEAT_FILE is required")
	}

	if cfg.batchTimeout >= cfg.heartbeatMaxAge || cfg.pollInterval >= cfg.heartbeatMaxAge-cfg.batchTimeout {
		return config{}, fmt.Errorf("E2B_WORKER_HEARTBEAT_MAX_AGE must exceed the batch timeout plus polling interval")
	}

	return cfg, nil
}
