//go:build integration

package inbox_test

import (
	"context"
	"fmt"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/usage"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BenchmarkPostgresInsertBatch measures committed, uniquely keyed batches in
// private schemas under concurrent writers and several connection budgets.
// It reports throughput and p95 batch latency for this machine; HTTP parsing,
// accounting, sustained retention, retries, and production capacity are excluded.
func BenchmarkPostgresInsertBatch(b *testing.B) {
	workloads := []struct {
		name             string
		connections      int32
		eventsPerBatch   int
		wantRowsPerBatch int
	}{
		{
			name:             "connections=4/events=100",
			connections:      4,
			eventsPerBatch:   100,
			wantRowsPerBatch: 100,
		},
		{
			name:             "connections=4/events=1000",
			connections:      4,
			eventsPerBatch:   1000,
			wantRowsPerBatch: 1000,
		},
		{
			name:             "connections=8/events=100",
			connections:      8,
			eventsPerBatch:   100,
			wantRowsPerBatch: 100,
		},
		{
			name:             "connections=8/events=1000",
			connections:      8,
			eventsPerBatch:   1000,
			wantRowsPerBatch: 1000,
		},
		{
			name:             "connections=16/events=100",
			connections:      16,
			eventsPerBatch:   100,
			wantRowsPerBatch: 100,
		},
		{
			name:             "connections=16/events=1000",
			connections:      16,
			eventsPerBatch:   1000,
			wantRowsPerBatch: 1000,
		},
	}
	for _, tt := range workloads {
		b.Run(tt.name, func(b *testing.B) {
			connections, size := tt.connections, tt.eventsPerBatch
			admin := testDatabase(b, nil)
			config := admin.Config()
			config.MaxConns, config.MinConns = connections, 0
			pool, err := pgxpool.NewWithConfig(context.Background(), config)
			if err != nil {
				b.Fatalf("Open benchmark pool: %v", err)
			}
			b.Cleanup(pool.Close)
			store := inbox.NewPostgres(pool, 5*time.Second)
			fixture := fixtureEvents()[0]
			receipt := time.Now().UTC()
			durations := make([]time.Duration, b.N)
			var next atomic.Uint64
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					index := next.Add(1) - 1
					events := make([]usage.Event, size)
					for i := range events {
						events[i] = fixture
						events[i].EventID = fmt.Sprintf("batch-%012d-event-%04d", index, i)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					start := time.Now()
					err := store.InsertBatch(ctx, events, receipt)
					durations[index] = time.Since(start)
					cancel()
					if err != nil {
						b.Errorf("Commit benchmark batch: %v", err)
						return
					}
				}
			})
			b.StopTimer()
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			b.ReportMetric(float64(size*b.N)/b.Elapsed().Seconds(), "events/s")
			b.ReportMetric(float64(durations[len(durations)-1-len(durations)/20])/float64(time.Millisecond), "p95-ms")
			if count := eventCount(b, pool); count != b.N*tt.wantRowsPerBatch {
				b.Errorf("Persisted rows = %d; want %d", count, b.N*tt.wantRowsPerBatch)
			}
		})
	}
}
