package postgres

import (
	"context"
	"e2b/billing-api/internal/usage"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"sort"
	"time"
)

// ConflictError identifies a reused measurement key with different content.
// Receipt time and processing state are not part of measurement identity.
type ConflictError struct {
	Source  string
	EventID string
}

func (err *ConflictError) Error() string {
	return fmt.Sprintf("different measurement content for (%q, %q)", err.Source, err.EventID)
}

// InsertBatch validates values and commits the entire batch or rolls it back.
// The caller supplies receipt time explicitly; it is normalized to UTC at
// PostgreSQL precision. Identical retries leave every stored value unchanged.
func (store *Store) InsertBatch(ctx context.Context, events []usage.Event, receivedAt time.Time) error {
	if err := validateInboxBatch(events, receivedAt); err != nil {
		return err
	}

	unique, err := orderedUniqueEvents(events)
	if err != nil {
		return err
	}

	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin inbox transaction: %w", err)
	}
	defer store.rollback(transaction)
	if _, err := transaction.Exec(ctx, "SET LOCAL synchronous_commit = on"); err != nil {
		return fmt.Errorf("configure durable inbox commit: %w", err)
	}

	receivedAt = receivedAt.UTC().Truncate(time.Microsecond)
	for index, event := range unique {
		if err := insertInboxEvent(ctx, transaction, event, receivedAt, index); err != nil {
			return err
		}
	}

	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit inbox batch: %w", err)
	}

	return nil
}

func validateInboxBatch(events []usage.Event, receivedAt time.Time) error {
	if len(events) == 0 {
		return errors.New("usage batch must contain at least one event")
	}

	if receivedAt.IsZero() || receivedAt.UTC().Year() < usage.MinUTCYear || receivedAt.UTC().Year() > usage.MaxUTCYear {
		return fmt.Errorf("received_at must be supplied with a UTC year between %d and %d", usage.MinUTCYear, usage.MaxUTCYear)
	}

	for index, event := range events {
		if err := event.Validate(); err != nil {
			return fmt.Errorf("validate inbox event %d: %w", index, err)
		}
	}

	return nil
}

func orderedUniqueEvents(events []usage.Event) ([]usage.Event, error) {
	// All writers acquire event keys in the same order, even when clients send
	// overlapping batches in opposite order. Never reorder the caller's slice.
	ordered := append([]usage.Event(nil), events...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Source != ordered[j].Source {
			return ordered[i].Source < ordered[j].Source
		}

		return ordered[i].EventID < ordered[j].EventID
	})
	unique := ordered[:0]
	for _, event := range ordered {
		if len(unique) > 0 {
			previous := unique[len(unique)-1]
			if previous.Source == event.Source && previous.EventID == event.EventID {
				if !sameMeasurement(previous, event) {
					return nil, &ConflictError{event.Source, event.EventID}
				}

				continue
			}
		}

		unique = append(unique, event)
	}

	return unique, nil
}

func insertInboxEvent(ctx context.Context, transaction pgx.Tx, event usage.Event, receivedAt time.Time, index int) error {
	result, err := transaction.Exec(ctx, `
			INSERT INTO usage_inbox (
				source, event_id, schema_version, customer_id, sandbox_id, metric,
				period_start, period_end, units, received_at, processed_at, processing_error
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULL, NULL)
			ON CONFLICT (source, event_id) DO NOTHING`,
		event.Source, event.EventID, event.SchemaVersion, event.CustomerID,
		event.SandboxID, event.Metric, event.PeriodStart, event.PeriodEnd, event.Units, receivedAt)
	if err != nil {
		return fmt.Errorf("insert inbox event %d: %w", index, err)
	}

	if result.RowsAffected() == 0 {
		return compareInboxEvent(ctx, transaction, event, index)
	}

	return nil
}

// A competing insert may have committed after INSERT's snapshot. A separate
// READ COMMITTED statement sees the winner; a shared lock preserves its checked
// content until commit without changing receipt or accounting state on retry.
func compareInboxEvent(ctx context.Context, transaction pgx.Tx, event usage.Event, index int) error {
	var identical bool
	err := transaction.QueryRow(ctx, `
				SELECT schema_version = $3 AND customer_id = $4 AND sandbox_id = $5
					AND metric = $6 AND period_start = $7 AND period_end = $8 AND units = $9
				FROM usage_inbox WHERE source = $1 AND event_id = $2
				FOR SHARE`, event.Source, event.EventID, event.SchemaVersion, event.CustomerID,
		event.SandboxID, event.Metric, event.PeriodStart, event.PeriodEnd, event.Units).Scan(&identical)
	if err != nil {
		return fmt.Errorf("compare inbox event %d: %w", index, err)
	}

	if !identical {
		return &ConflictError{event.Source, event.EventID}
	}

	return nil
}

// sameMeasurement compares domain values, including timestamp instants rather
// than textual offsets. Transport metadata and accounting state are excluded.
func sameMeasurement(first, second usage.Event) bool {
	return first.SchemaVersion == second.SchemaVersion &&
		first.CustomerID == second.CustomerID && first.SandboxID == second.SandboxID &&
		first.Metric == second.Metric && first.Units == second.Units &&
		first.PeriodStart.Equal(second.PeriodStart) && first.PeriodEnd.Equal(second.PeriodEnd)
}
