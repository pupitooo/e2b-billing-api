package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const idempotencyLockNamespace = 65_103

// operationResponse locks a scoped request before inspecting committed history.
// Hash collisions only serialize unrelated requests; the primary key determines
// identity. The caller retains this lock through the financial write and commit.
func operationResponse(ctx context.Context, tx pgx.Tx, scope, key string, target any) (bool, error) {
	identity, err := json.Marshal([]string{scope, key})
	if err != nil {
		return false, err
	}

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1, hashtext($2))", idempotencyLockNamespace, string(identity)); err != nil {
		return false, err
	}

	var encoded []byte
	err = tx.QueryRow(ctx, `SELECT response_payload FROM api_idempotency_operations
        WHERE operation_scope=$1 AND idempotency_key=$2`, scope, key).Scan(&encoded)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	return true, json.Unmarshal(encoded, target)
}

// recordOperation freezes the original request and successful result inside the
// financial transaction. Failed commands leave neither an effect nor a key.
func recordOperation(ctx context.Context, tx pgx.Tx, scope, key string, request, response any, createdAt time.Time) error {
	input, err := json.Marshal(request)
	if err != nil {
		return err
	}

	output, err := json.Marshal(response)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `INSERT INTO api_idempotency_operations
        (operation_scope,idempotency_key,request_payload,response_payload,created_at)
        VALUES ($1,$2,$3,$4,$5)`, scope, key, input, output, createdAt.UTC())

	return err
}
