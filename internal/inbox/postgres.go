// Package inbox stores platform measurements before accounting processes them.
package inbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"e2b/billing-api/internal/usage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OpenPool establishes PostgreSQL connections with UTC sessions. An empty
// connection string uses the standard PG environment variables. Migrations
// remain explicit; the caller owns the returned pool and must close it.
func OpenPool(ctx context.Context, connectionString string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(connectionString)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL connection configuration")
	}
	config.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create inbox connection pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to inbox database: %w", err)
	}
	return pool, nil
}

// Postgres stores complete batches in the existing usage_inbox schema.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres uses the caller's pool without taking ownership of its lifetime.
func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

// InsertBatch validates values and commits the entire batch or rolls it back.
// The caller supplies receipt time explicitly; it is normalized to UTC at
// PostgreSQL precision. Idempotent duplicate handling is the next ingestion step.
func (store *Postgres) InsertBatch(ctx context.Context, events []usage.Event, receivedAt time.Time) error {
	if len(events) == 0 {
		return errors.New("usage batch must contain at least one event")
	}
	if receivedAt.IsZero() || receivedAt.UTC().Year() < 1 || receivedAt.UTC().Year() > 9999 {
		return errors.New("received_at must be supplied with a UTC year between 1 and 9999")
	}
	for index, event := range events {
		if err := event.Validate(); err != nil {
			return fmt.Errorf("validate inbox event %d: %w", index, err)
		}
	}
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin inbox transaction: %w", err)
	}
	defer func() {
		// Cancellation must not prevent cleanup of an already-open transaction.
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = transaction.Rollback(cleanupContext)
	}()
	if _, err := transaction.Exec(ctx, "SET LOCAL synchronous_commit = on"); err != nil {
		return fmt.Errorf("configure durable inbox commit: %w", err)
	}
	receivedAt = receivedAt.UTC().Truncate(time.Microsecond)
	for index, event := range events {
		_, err := transaction.Exec(ctx, `
			INSERT INTO usage_inbox (
				source, event_id, schema_version, customer_id, sandbox_id, metric,
				period_start, period_end, units, received_at, processed_at, processing_error
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULL, NULL)`,
			event.Source, event.EventID, event.SchemaVersion, event.CustomerID,
			event.SandboxID, event.Metric, event.PeriodStart, event.PeriodEnd,
			event.Units, receivedAt,
		)
		if err != nil {
			return fmt.Errorf("insert inbox event %d: %w", index, err)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit inbox batch: %w", err)
	}
	return nil
}
