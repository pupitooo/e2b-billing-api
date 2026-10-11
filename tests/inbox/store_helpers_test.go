//go:build integration

package inbox_test

import (
	"e2b/billing-api/internal/httpapi"
	"e2b/billing-api/internal/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"testing"
	"time"
)

// newPostgresStore constructs persistence against the scenario's private pool.
// Clock and cleanup inputs stay explicit at the call site; setup errors fail it.
func newPostgresStore(t testing.TB, pool *pgxpool.Pool, config postgres.Config) *postgres.Store {
	t.Helper()
	store, err := postgres.NewStore(pool, config)
	if err != nil {
		t.Fatalf("NewStore(%+v): %v", config, err)
	}
	return store
}

// newBillingHandler registers the complete API against the scenario's one store.
// Endpoint inputs and expected responses remain in the calling scenario.
func newBillingHandler(t testing.TB, store *postgres.Store, timeout time.Duration, maxInFlight int) http.Handler {
	t.Helper()
	return newBillingHandlerWithClock(t, store, timeout, maxInFlight, nil)
}

// newBillingHandlerWithClock shares the scenario business clock with receipt
// ingestion and current-month transport decisions without changing runtime timers.
func newBillingHandlerWithClock(t testing.TB, store *postgres.Store, timeout time.Duration, maxInFlight int, now func() time.Time) http.Handler {
	t.Helper()
	handler, err := httpapi.NewHandler(store, timeout, maxInFlight, now)
	if err != nil {
		t.Fatalf("NewHandler(timeout=%s, maxInFlight=%d): %v", timeout, maxInFlight, err)
	}
	return handler
}
