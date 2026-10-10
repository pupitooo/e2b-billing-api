package billing

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"e2b/billing-api/internal/accounting"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	priceOperationScope = "prices"
	priceIDPrefix       = "price_"
)

// PriceInput contains caller-owned price values; resource identities belong to billing.
// An empty customer denotes the default price. CreatePriceNow resolves the timestamp.
type PriceInput struct {
	CustomerID           string    `json:"customer_id"`
	Metric               string    `json:"metric"`
	PricePerMillionCents int64     `json:"price_per_million_cents"`
	EffectiveFrom        time.Time `json:"effective_from"`
}

// CreatePrice generates an identity and appends a version under the catalog lock.
// The scoped request key replays the stored result even after activation.
func (s *Store) CreatePrice(ctx context.Context, key string, input PriceInput) (accounting.PriceVersion, error) {
	return s.createPrice(ctx, key, input, false)
}

// CreatePriceNow resolves activation under the catalog lock. Replays retain both
// the server-generated identity and the original persisted activation instant.
func (s *Store) CreatePriceNow(ctx context.Context, key string, input PriceInput) (accounting.PriceVersion, error) {
	return s.createPrice(ctx, key, input, true)
}

func (s *Store) createPrice(ctx context.Context, key string, input PriceInput, useCurrentTime bool) (accounting.PriceVersion, error) {
	if err := ValidateIdempotencyKey(key); err != nil {
		return accounting.PriceVersion{}, err
	}

	if err := validatePriceValues(input); err != nil {
		return accounting.PriceVersion{}, err
	}

	if !useCurrentTime {
		if err := validateTime("effective_from", input.EffectiveFrom); err != nil {
			return accounting.PriceVersion{}, err
		}
	}

	input.EffectiveFrom = input.EffectiveFrom.UTC()
	price := accounting.PriceVersion{CustomerID: input.CustomerID, Metric: input.Metric,
		PricePerMillionCents: input.PricePerMillionCents, EffectiveFrom: input.EffectiveFrom}
	err := s.transact(ctx, func(tx pgx.Tx) error {
		var existing accounting.PriceVersion
		found, err := operationResponse(ctx, tx, priceOperationScope, key, &existing)
		if err != nil {
			return err
		}

		if found {
			price.ID = existing.ID
			if useCurrentTime {
				price.EffectiveFrom = existing.EffectiveFrom
			}

			if !samePrice(existing, price) {
				return ErrConflict
			}

			price = existing

			return nil
		}

		if err := catalogLock(ctx, tx, true); err != nil {
			return err
		}

		insertionTime := s.now().UTC()
		if useCurrentTime {
			price.EffectiveFrom = insertionTime.Truncate(time.Microsecond)
			if err := validateTime("effective_from", price.EffectiveFrom); err != nil {
				return err
			}
		} else if price.EffectiveFrom.Before(insertionTime) {
			return &ValidationError{Field: "effective_from", Message: "A new price must not start before its insertion time."}
		}

		price.ID = priceIDPrefix + rand.Text()
		if err := preserveRatedHistory(ctx, tx, price); err != nil {
			return err
		}

		if err := insertPrice(ctx, tx, price); err != nil {
			return err
		}

		request := struct {
			Price          PriceInput `json:"price"`
			UseCurrentTime bool       `json:"use_current_time"`
		}{Price: input, UseCurrentTime: useCurrentTime}

		return recordOperation(ctx, tx, priceOperationScope, key, request, price, insertionTime)
	})
	if err != nil {
		return accounting.PriceVersion{}, err
	}

	return price, nil
}

func insertPrice(ctx context.Context, tx pgx.Tx, price accounting.PriceVersion) error {
	var owner any
	if price.CustomerID != "" {
		owner = price.CustomerID
	}

	_, err := tx.Exec(ctx, `INSERT INTO price_versions VALUES ($1,$2,$3,$4,$5)`,
		price.ID, owner, price.Metric, price.PricePerMillionCents, price.EffectiveFrom)
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) {
		if databaseError.Code == "23505" {
			return ErrConflict
		}

		if databaseError.Code == "23503" {
			return ErrNotFound
		}
	}

	return err
}

func validatePriceValues(p PriceInput) error {
	if err := ValidateIdentifier("metric", p.Metric); err != nil {
		return err
	}

	if p.CustomerID != "" {
		if err := ValidateIdentifier("customer_id", p.CustomerID); err != nil {
			return err
		}
	}

	return validateCents("price_per_million_cents", p.PricePerMillionCents, false)
}

func samePrice(a, b accounting.PriceVersion) bool {
	return a.ID == b.ID && a.CustomerID == b.CustomerID && a.Metric == b.Metric &&
		a.PricePerMillionCents == b.PricePerMillionCents && a.EffectiveFrom.Equal(b.EffectiveFrom)
}

// preserveRatedHistory checks both selected IDs and interval boundaries. A new
// default masked by a customer's override is harmless and remains permitted.
func preserveRatedHistory(ctx context.Context, tx pgx.Tx, proposed accounting.PriceVersion) error {
	rows, err := tx.Query(ctx, `SELECT i.source,i.event_id,i.schema_version,i.customer_id,i.sandbox_id,
        i.metric,i.period_start,i.period_end,i.units,g.price_version_id
        FROM usage_inbox i JOIN usage_ratings r USING(source,event_id)
        JOIN rated_usage_groups g USING(group_id)
        WHERE i.metric=$1 AND i.period_end>$2 AND ($3='' OR i.customer_id=$3)`,
		proposed.Metric, proposed.EffectiveFrom, proposed.CustomerID)
	if err != nil {
		return err
	}

	var affected []struct {
		item    receipt
		priceID string
	}
	for rows.Next() {
		var item struct {
			item    receipt
			priceID string
		}
		e := &item.item.event
		if err := rows.Scan(&e.Source, &e.EventID, &e.SchemaVersion, &e.CustomerID, &e.SandboxID, &e.Metric, &e.PeriodStart, &e.PeriodEnd, &e.Units, &item.priceID); err != nil {
			rows.Close()
			return err
		}

		affected = append(affected, item)
	}

	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, record := range affected {
		prices, err := loadPrices(ctx, tx, record.item.event.CustomerID, proposed.Metric)
		if err != nil {
			return err
		}

		rated, err := accounting.Rate(record.item.event, append(prices, proposed))
		if err != nil || rated.Price.ID != record.priceID {
			return ErrConflict
		}
	}

	return nil
}
