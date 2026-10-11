package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// transact guarantees durable commit and cleanup independent of cancellation.
func (s *Store) transact(ctx context.Context, action func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}

	defer s.rollback(tx)
	if _, err := tx.Exec(ctx, "SET LOCAL synchronous_commit = on"); err != nil {
		return err
	}

	if err := action(tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// rollback bounds cleanup independently of cancellation for every transaction.
func (store *Store) rollback(transaction pgx.Tx) {
	cleanupContext, cancel := context.WithTimeout(context.Background(), store.rollbackTimeout)
	defer cancel()
	_ = transaction.Rollback(cleanupContext)
}
