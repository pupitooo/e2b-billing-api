// Package billing persists financial effects under one lock per customer.
package billing

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"e2b/billing-api/internal/accounting"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	transactionRollbackTimeout = 5 * time.Second
	decimalRadix               = 10

	// The namespace is shared with migrations; this key identifies catalog access.
	catalogLockNamespace = 65_102
	catalogLockKey       = 2
)

var (
	ErrNotFound = errors.New("billing resource not found")
	ErrConflict = errors.New("operation conflicts with financial history")
)

// Store borrows its pool; the process owner controls the pool's lifetime.
type Store struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// NewStore uses the real server clock for closing and invoice timestamps.
func NewStore(pool *pgxpool.Pool) *Store {
	return NewStoreWithClock(pool, time.Now)
}

// NewStoreWithClock makes invoice time explicit for deterministic simulations.
// Production callers use NewStore; HTTP requests never supply the clock.
func NewStoreWithClock(pool *pgxpool.Pool, now func() time.Time) *Store {
	return &Store{pool: pool, now: now}
}

// transact guarantees durable commit and cleanup independent of cancellation.
func (s *Store) transact(ctx context.Context, action func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), transactionRollbackTimeout)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if _, err := tx.Exec(ctx, "SET LOCAL synchronous_commit = on"); err != nil {
		return err
	}

	if err := action(tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// lockAccount precedes receipt locks and every mutable financial read.
func lockAccount(ctx context.Context, tx pgx.Tx, customer string) (accounting.Amount, error) {
	var ticks string
	err := tx.QueryRow(ctx, `SELECT credit_balance_ticks::text FROM customer_billing_state
        WHERE customer_id=$1 FOR UPDATE`, customer).Scan(&ticks)
	if errors.Is(err, pgx.ErrNoRows) {
		return accounting.Amount{}, ErrNotFound
	}

	if err != nil {
		return accounting.Amount{}, err
	}

	return amount(ticks)
}

// amount rejects corrupt database values rather than silently rounding them.
func amount(ticks string) (accounting.Amount, error) {
	value, ok := new(big.Int).SetString(ticks, decimalRadix)
	if !ok {
		return accounting.Amount{}, fmt.Errorf("invalid integer ticks: %q", ticks)
	}

	return accounting.FromTicks(value)
}

// identity hashes a structured key so caller separators cannot cause collisions.
func identity(prefix string, parts ...string) string {
	encoded, _ := json.Marshal(parts)
	return fmt.Sprintf("%s%x", prefix, sha256.Sum256(encoded))
}

// catalogLock serializes price insertion against rating and historical checks.
func catalogLock(ctx context.Context, tx pgx.Tx, exclusive bool) error {
	command := "SELECT pg_advisory_xact_lock_shared($1, $2)"
	if exclusive {
		command = "SELECT pg_advisory_xact_lock($1, $2)"
	}

	_, err := tx.Exec(ctx, command, catalogLockNamespace, catalogLockKey)

	return err
}
