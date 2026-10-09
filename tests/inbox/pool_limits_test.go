//go:build integration

package inbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2b/billing-api/internal/inbox"
)

// TestPoolConnectionBudget reserves two real connections, then attempts a
// third acquisition. Explicit limits must override DSN settings, bound waiting
// by the caller's deadline, and permit reuse after a checked-out slot is freed.
func TestPoolConnectionBudget(t *testing.T) {
	admin := testDatabase(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := inbox.OpenPool(ctx, admin.Config().ConnString(), inbox.PoolLimits{MaxConns: 2, MinConns: 0})
	if err != nil {
		t.Fatalf("Open limited pool: %v", err)
	}
	t.Cleanup(pool.Close)
	first, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire first connection: %v", err)
	}
	t.Cleanup(first.Release)
	second, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire second connection: %v", err)
	}
	t.Cleanup(second.Release)
	wait, cancelWait := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancelWait()
	third, err := pool.Acquire(wait)
	if third != nil {
		third.Release()
	}
	if !errors.Is(err, context.DeadlineExceeded) || pool.Stat().TotalConns() != 2 {
		t.Fatalf("Third acquisition = %v, connections %d; want bounded wait with two connections", err, pool.Stat().TotalConns())
	}
	first.Release()
	if err := pool.Ping(ctx); err != nil {
		t.Errorf("Pool unusable after canceled acquisition: %v", err)
	}
}
