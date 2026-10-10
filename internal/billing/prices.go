package billing

import (
	"context"
	"errors"

	"e2b/billing-api/internal/accounting"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// CreatePrice appends a version under the catalog lock. Identical identities
// replay successfully; a new version cannot invalidate any persisted rating.
func (s *Store) CreatePrice(ctx context.Context, price accounting.PriceVersion) error {
	if err := validatePrice(price); err != nil {
		return err
	}

	return s.transact(ctx, func(tx pgx.Tx) error {
		if err := catalogLock(ctx, tx, true); err != nil {
			return err
		}

		existing, err := priceByID(ctx, tx, price.ID)
		if err == nil {
			if samePrice(existing, price) {
				return nil
			}

			return ErrConflict
		}

		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		if err := preserveRatedHistory(ctx, tx, price); err != nil {
			return err
		}

		var owner any
		if price.CustomerID != "" {
			owner = price.CustomerID
		}

		_, err = tx.Exec(ctx, `INSERT INTO price_versions VALUES ($1,$2,$3,$4,$5)`,
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
	})
}

func validatePrice(p accounting.PriceVersion) error {
	for field, value := range map[string]string{"price_version_id": p.ID, "metric": p.Metric} {
		if err := ValidateIdentifier(field, value); err != nil {
			return err
		}
	}

	if p.CustomerID != "" {
		if err := ValidateIdentifier("customer_id", p.CustomerID); err != nil {
			return err
		}
	}

	if err := validateTime("effective_from", p.EffectiveFrom); err != nil {
		return err
	}

	return validateCents("price_per_million_cents", p.PricePerMillionCents, false)
}

func priceByID(ctx context.Context, tx pgx.Tx, id string) (accounting.PriceVersion, error) {
	var p accounting.PriceVersion
	err := tx.QueryRow(ctx, `SELECT price_version_id,COALESCE(customer_id,''),metric,
        price_per_million_cents,effective_from FROM price_versions WHERE price_version_id=$1`, id).
		Scan(&p.ID, &p.CustomerID, &p.Metric, &p.PricePerMillionCents, &p.EffectiveFrom)

	return p, err
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
