package postgres

import (
	"context"
	"crypto/rand"
	"errors"

	"e2b/billing-api/internal/accounting"
	"e2b/billing-api/internal/billing"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	priceOperationScope = "prices"
	priceIDPrefix       = "price_"
)

// CreatePrice generates an identity and appends a version under the catalog lock.
// The scoped request key replays the stored result even after activation.
func (s *Store) CreatePrice(ctx context.Context, key string, input billing.PriceInput) (accounting.PriceVersion, error) {
	if err := billing.ValidateIdempotencyKey(key); err != nil {
		return accounting.PriceVersion{}, err
	}

	if err := validatePriceValues(input); err != nil {
		return accounting.PriceVersion{}, err
	}

	if err := billing.ValidateDate("effective_from", input.EffectiveFrom); err != nil {
		return accounting.PriceVersion{}, err
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

			if !samePrice(existing, price) {
				return billing.ErrConflict
			}

			price = existing

			return nil
		}

		if err := catalogLock(ctx, tx, true); err != nil {
			return err
		}

		insertionTime := s.now().UTC()
		if price.EffectiveFrom.Before(billing.UTCDay(insertionTime)) {
			return &billing.ValidationError{Field: "effective_from", Message: "A new price must not start before the current UTC date."}
		}

		price.ID = priceIDPrefix + rand.Text()
		if err := preserveRatedHistory(ctx, tx, price); err != nil {
			return err
		}

		if err := insertPrice(ctx, tx, price); err != nil {
			return err
		}

		return recordOperation(ctx, tx, priceOperationScope, key, input, price, insertionTime)
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
			return billing.ErrConflict
		}

		if databaseError.Code == "23503" {
			return billing.ErrNotFound
		}
	}

	return err
}

func validatePriceValues(p billing.PriceInput) error {
	if err := billing.ValidateIdentifier("metric", p.Metric); err != nil {
		return err
	}

	if p.CustomerID != "" {
		if err := billing.ValidateIdentifier("customer_id", p.CustomerID); err != nil {
			return err
		}
	}

	return billing.ValidateCents("price_per_million_cents", p.PricePerMillionCents, false)
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
			return billing.ErrConflict
		}
	}

	return nil
}
