package postgres

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"e2b/billing-api/internal/accounting"
	"e2b/billing-api/internal/billing"
	"github.com/jackc/pgx/v5"
)

const (
	decimalRadix = 10

	// The namespace is shared with migrations; this key identifies catalog access.
	catalogLockNamespace = 65_102
	catalogLockKey       = 2
)

// lockAccount precedes receipt locks and every mutable financial read.
func lockAccount(ctx context.Context, tx pgx.Tx, customer string) (accounting.Amount, error) {
	var ticks string
	err := tx.QueryRow(ctx, `SELECT credit_balance_ticks::text FROM customer_billing_state
        WHERE customer_id=$1 FOR UPDATE`, customer).Scan(&ticks)
	if errors.Is(err, pgx.ErrNoRows) {
		return accounting.Amount{}, billing.ErrNotFound
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

// catalogLock serializes price insertion against rating and historical checks.
func catalogLock(ctx context.Context, tx pgx.Tx, exclusive bool) error {
	command := "SELECT pg_advisory_xact_lock_shared($1, $2)"
	if exclusive {
		command = "SELECT pg_advisory_xact_lock($1, $2)"
	}

	_, err := tx.Exec(ctx, command, catalogLockNamespace, catalogLockKey)

	return err
}
