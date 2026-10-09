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

// BenchmarkInboxInsertBatch measures committed, uniquely keyed batches in
// private schemas under concurrent writers and several connection budgets.
// It reports throughput and p95 batch latency for this machine; HTTP parsing,
// accounting, sustained retention, retries, and production capacity are excluded.
func BenchmarkInboxInsertBatch(b *testing.B) {
	for _, connections := range []int32{4, 8, 16} {
		for _, size := range []int{100, 1_000} {
			b.Run(fmt.Sprintf("connections=%d/events=%d", connections, size), func(b *testing.B) {
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
			})
		}
	}
}
