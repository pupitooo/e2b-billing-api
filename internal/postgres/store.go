// Package postgres persists measurements and financial effects in one PostgreSQL database.
package postgres

import (
	"errors"
	"time"

	"e2b/billing-api/internal/billingclock"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultRollbackTimeout bounds transaction cleanup independently of cancellation.
const DefaultRollbackTimeout = 5 * time.Second

// Config applies the same clock and cleanup budget to all storage operations.
// Zero values select the real server clock and the default rollback timeout.
type Config struct {
	RollbackTimeout time.Duration
	Now             func() time.Time
}

// Store borrows its pool; the process owner controls the pool's lifetime.
type Store struct {
	pool            *pgxpool.Pool
	now             func() time.Time
	rollbackTimeout time.Duration
}

// NewStore requires a pool and configures receipt and financial persistence together.
// HTTP requests never supply the clock; tests may use Config.Now for fixed time.
func NewStore(pool *pgxpool.Pool, config Config) (*Store, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}

	if config.RollbackTimeout < 0 {
		return nil, errors.New("PostgreSQL rollback timeout must not be negative")
	}

	if config.RollbackTimeout == 0 {
		config.RollbackTimeout = DefaultRollbackTimeout
	}

	if config.Now == nil {
		config.Now = billingclock.RealNow
	}

	return &Store{pool: pool, now: config.Now, rollbackTimeout: config.RollbackTimeout}, nil
}
