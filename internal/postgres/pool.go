package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolLimits reserves a finite connection budget for one application process.
type PoolLimits struct {
	MaxConns int32
	MinConns int32
}

// OpenPool establishes PostgreSQL connections with UTC sessions. An empty
// connection string uses the standard PG environment variables. Migrations
// remain explicit; the caller owns the returned pool and must close it.
// An optional limit overrides pool sizes from the connection string.
func OpenPool(ctx context.Context, connectionString string, limits ...PoolLimits) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(connectionString)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL connection configuration")
	}

	if len(limits) > 1 {
		return nil, errors.New("only one PostgreSQL pool limit may be supplied")
	}

	if len(limits) == 1 {
		limit := limits[0]
		if limit.MaxConns <= 0 || limit.MinConns < 0 || limit.MinConns > limit.MaxConns {
			return nil, errors.New("PostgreSQL pool requires positive maximum connections and minimum between zero and maximum")
		}

		config.MaxConns, config.MinConns = limit.MaxConns, limit.MinConns
		// MinIdleConns can also be specified in the DSN and must fit the budget.
		if config.MinIdleConns > config.MaxConns {
			return nil, errors.New("pool_min_idle_conns exceeds the PostgreSQL connection budget")
		}
	}

	config.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to PostgreSQL database: %w", err)
	}

	return pool, nil
}
