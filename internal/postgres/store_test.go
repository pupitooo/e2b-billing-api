package postgres

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestNewStore checks required connections, shared cleanup defaults, and the
// explicit clock used by financial writes without opening database connections.
func TestNewStore(t *testing.T) {
	cases := []struct {
		name                string
		pool                *pgxpool.Pool
		rollbackTimeout     time.Duration
		clock               func() time.Time
		wantError           bool
		wantErrorMessage    string
		wantRollbackTimeout time.Duration
		wantClockTime       time.Time
		wantRealClock       bool
	}{
		{name: "missing pool", pool: nil, wantError: true, wantErrorMessage: "PostgreSQL pool is required"},
		{name: "negative cleanup budget", pool: new(pgxpool.Pool), rollbackTimeout: -time.Nanosecond, wantError: true, wantErrorMessage: "PostgreSQL rollback timeout must not be negative"},
		{name: "zero settings select defaults", pool: new(pgxpool.Pool), rollbackTimeout: 0, wantRollbackTimeout: 5 * time.Second, wantRealClock: true},
		{name: "smallest positive cleanup budget", pool: new(pgxpool.Pool), rollbackTimeout: time.Nanosecond, wantRollbackTimeout: time.Nanosecond, wantRealClock: true},
		{
			name: "explicit shared settings", pool: new(pgxpool.Pool), rollbackTimeout: 2 * time.Second,
			clock:               func() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) },
			wantRollbackTimeout: 2 * time.Second, wantClockTime: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := time.Now()
			store, err := NewStore(tc.pool, Config{RollbackTimeout: tc.rollbackTimeout, Now: tc.clock})

			if (err != nil) != tc.wantError {
				t.Fatalf("NewStore(pool=%p, rollback=%s): error=%v; wantError=%t", tc.pool, tc.rollbackTimeout, err, tc.wantError)
			}

			if tc.wantError {
				if err.Error() != tc.wantErrorMessage {
					t.Errorf("NewStore(pool=%p, rollback=%s): error=%q; want %q", tc.pool, tc.rollbackTimeout, err.Error(), tc.wantErrorMessage)
				}

				if store != nil {
					t.Errorf("NewStore(pool=%p, rollback=%s): store=%v; want nil", tc.pool, tc.rollbackTimeout, store)
				}

				return
			}

			if store.pool != tc.pool {
				t.Errorf("NewStore(pool=%p): pool=%p; want %p", tc.pool, store.pool, tc.pool)
			}

			if store.rollbackTimeout != tc.wantRollbackTimeout {
				t.Errorf("NewStore(rollback=%s): cleanup budget=%s; want %s", tc.rollbackTimeout, store.rollbackTimeout, tc.wantRollbackTimeout)
			}

			actualTime := store.now()
			if tc.wantRealClock {
				after := time.Now()
				if actualTime.Before(before) || actualTime.After(after) {
					t.Errorf("NewStore(default clock): time=%s; want between %s and %s", actualTime, before, after)
				}
			} else if !actualTime.Equal(tc.wantClockTime) {
				t.Errorf("NewStore(explicit clock): time=%s; want %s", actualTime, tc.wantClockTime)
			}
		})
	}
}
