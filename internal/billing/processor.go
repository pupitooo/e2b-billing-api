package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"e2b/billing-api/internal/accounting"
	"e2b/billing-api/internal/usage"
	"github.com/jackc/pgx/v5"
)

const missingPriceErrorCode = "missing_valid_price"
const missingPricePriority = "P0"

type receipt struct {
	event      usage.Event
	receivedAt time.Time
}

// ProcessBatch accounts for one receipt. Concurrent workers may select the same
// candidate, but serialize on its account before locking or changing the receipt.
// Unsupported input is quarantined visibly; database failures roll back for retry.
// A missing valid price commits its P0 error before emitting an operational report.
func (s *Store) ProcessBatch(ctx context.Context) (bool, error) {
	var candidate receipt
	err := s.pool.QueryRow(ctx, `SELECT source,event_id,customer_id FROM usage_inbox
        WHERE processed_at IS NULL AND processing_error IS NULL
        ORDER BY received_at,source,event_id LIMIT 1`).Scan(
		&candidate.event.Source, &candidate.event.EventID, &candidate.event.CustomerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	err = s.accountReceipt(ctx, candidate)

	return err == nil, err
}

// accountReceipt reports catalog incidents only after the quarantine commits.
// Both polling and invoice closing use this transaction and reporting boundary.
func (s *Store) accountReceipt(ctx context.Context, candidate receipt) error {
	var missingPrice *accounting.MissingPriceError
	err := s.transact(ctx, func(tx pgx.Tx) error {
		var err error
		missingPrice, err = processReceipt(ctx, tx, candidate)

		return err
	})
	if err != nil {
		return err
	}

	if missingPrice != nil {
		slog.ErrorContext(ctx, "Usage has no valid price catalog; repair the catalog immediately",
			"priority", missingPricePriority, "error_code", missingPriceErrorCode,
			"source", candidate.event.Source, "event_id", candidate.event.EventID,
			"customer_id", missingPrice.CustomerID, "metric", missingPrice.Metric,
			"period_start", missingPrice.PeriodStart.UTC().Format(time.RFC3339Nano), "error", missingPrice)
	}

	return nil
}

// processReceipt keeps financial writes atomic and returns a catalog incident
// separately from transaction failure so its quarantine can commit without charges.
func processReceipt(ctx context.Context, tx pgx.Tx, candidate receipt) (*accounting.MissingPriceError, error) {
	credit, err := lockAccount(ctx, tx, candidate.event.CustomerID)
	if errors.Is(err, ErrNotFound) {
		return nil, quarantine(ctx, tx, candidate.event, "unknown customer")
	}

	if err != nil {
		return nil, err
	}

	if err := catalogLock(ctx, tx, false); err != nil {
		return nil, err
	}

	item, err := loadPendingReceipt(ctx, tx, candidate.event)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	prices, err := loadPrices(ctx, tx, item.event.CustomerID, item.event.Metric)
	if err != nil {
		return nil, err
	}

	rating, err := accounting.Rate(item.event, prices)
	if err != nil {
		var missingPrice *accounting.MissingPriceError
		if errors.As(err, &missingPrice) {
			message := fmt.Sprintf("%s %s: %s", missingPricePriority, missingPriceErrorCode, missingPrice)
			return missingPrice, quarantine(ctx, tx, item.event, message)
		}

		return nil, quarantine(ctx, tx, item.event, err.Error())
	}

	closed, err := routingMonths(ctx, tx, item.event)
	if err != nil {
		return nil, err
	}

	month, err := accounting.BillingMonth(rating.UsageMonth, item.receivedAt, closed)
	if err != nil {
		return nil, quarantine(ctx, tx, item.event, err.Error())
	}

	return nil, persistRating(ctx, tx, item, rating, month, credit)
}

func loadPendingReceipt(ctx context.Context, tx pgx.Tx, key usage.Event) (receipt, error) {
	var item receipt
	e := &item.event
	err := tx.QueryRow(ctx, `SELECT source,event_id,schema_version,customer_id,sandbox_id,
        metric,period_start,period_end,units,received_at FROM usage_inbox
        WHERE source=$1 AND event_id=$2 AND processed_at IS NULL AND processing_error IS NULL
        FOR UPDATE`, key.Source, key.EventID).Scan(&e.Source, &e.EventID, &e.SchemaVersion,
		&e.CustomerID, &e.SandboxID, &e.Metric, &e.PeriodStart, &e.PeriodEnd, &e.Units, &item.receivedAt)

	return item, err
}

func loadPrices(ctx context.Context, tx pgx.Tx, customer, metric string) ([]accounting.PriceVersion, error) {
	rows, err := tx.Query(ctx, `SELECT price_version_id,COALESCE(customer_id,''),metric,
        effective_from,price_per_million_cents FROM price_versions
        WHERE metric=$1 AND (customer_id IS NULL OR customer_id=$2)`, metric, customer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var prices []accounting.PriceVersion
	for rows.Next() {
		var p accounting.PriceVersion
		if err := rows.Scan(&p.ID, &p.CustomerID, &p.Metric, &p.EffectiveFrom, &p.PricePerMillionCents); err != nil {
			return nil, err
		}

		prices = append(prices, p)
	}

	return prices, rows.Err()
}

func loadClosedMonths(ctx context.Context, tx pgx.Tx, customer string) ([]time.Time, error) {
	rows, err := tx.Query(ctx, "SELECT billing_month FROM closed_billing_months WHERE customer_id=$1", customer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var months []time.Time
	for rows.Next() {
		var month time.Time
		if err := rows.Scan(&month); err != nil {
			return nil, err
		}

		months = append(months, month)
	}

	return months, rows.Err()
}

func quarantine(ctx context.Context, tx pgx.Tx, event usage.Event, message string) error {
	_, err := tx.Exec(ctx, `UPDATE usage_inbox SET processing_error=$3
        WHERE source=$1 AND event_id=$2 AND processed_at IS NULL AND processing_error IS NULL`,
		event.Source, event.EventID, message)

	return err
}

func persistRating(ctx context.Context, tx pgx.Tx, item receipt, rating accounting.Rating, month time.Time, credit accounting.Amount) error {
	e := item.event
	group := identity("group/", e.CustomerID, rating.Price.ID, rating.UsageMonth.Format("2006-01"), month.Format("2006-01"))
	allocation := accounting.AllocateCredit(rating.Charge, credit)
	gross, err := cumulativeGross(ctx, tx, group, rating.Charge)
	if err != nil {
		return err
	}

	cents, err := gross.RoundCents()
	if err != nil {
		return quarantine(ctx, tx, e, err.Error())
	}

	if err := updateGroup(ctx, tx, group, item, rating, month, cents, allocation.Used); err != nil {
		return err
	}

	if err := recordCreditDebit(ctx, tx, e, group, allocation.Used); err != nil {
		return err
	}

	if err := updateMonthlyUsage(ctx, tx, e.CustomerID, rating.UsageMonth, rating.Charge); err != nil {
		return err
	}

	if err := updateAccountCredit(ctx, tx, e.CustomerID, allocation.Remaining); err != nil {
		return err
	}

	return completeReceipt(ctx, tx, e)
}

func updateMonthlyUsage(ctx context.Context, tx pgx.Tx, customer string, month time.Time, charge accounting.Amount) error {
	_, err := tx.Exec(ctx, `INSERT INTO monthly_usage VALUES ($1,$2,$3::numeric)
        ON CONFLICT (customer_id,usage_month) DO UPDATE
        SET gross_charge_ticks=monthly_usage.gross_charge_ticks+EXCLUDED.gross_charge_ticks`,
		customer, month, charge.Ticks().String())

	return err
}

func updateAccountCredit(ctx context.Context, tx pgx.Tx, customer string, remaining accounting.Amount) error {
	_, err := tx.Exec(ctx, `UPDATE customer_billing_state SET credit_balance_ticks=$2::numeric,
		state_version=state_version+1 WHERE customer_id=$1`, customer, remaining.Ticks().String())

	return err
}

func completeReceipt(ctx context.Context, tx pgx.Tx, event usage.Event) error {
	_, err := tx.Exec(ctx, `UPDATE usage_inbox SET processed_at=$3 WHERE source=$1 AND event_id=$2`,
		event.Source, event.EventID, time.Now().UTC().Truncate(time.Microsecond))

	return err
}

func cumulativeGross(ctx context.Context, tx pgx.Tx, group string, charge accounting.Amount) (accounting.Amount, error) {
	var ticks string
	err := tx.QueryRow(ctx, "SELECT exact_charge_ticks::text FROM rated_usage_groups WHERE group_id=$1", group).Scan(&ticks)
	if errors.Is(err, pgx.ErrNoRows) {
		return charge, nil
	}

	if err != nil {
		return accounting.Amount{}, err
	}

	previous, err := amount(ticks)
	if err != nil {
		return accounting.Amount{}, err
	}

	return previous.Add(charge), nil
}

func updateGroup(ctx context.Context, tx pgx.Tx, group string, item receipt, rating accounting.Rating, month time.Time, cents int64, used accounting.Amount) error {
	e := item.event
	_, err := tx.Exec(ctx, `INSERT INTO rated_usage_groups
        (group_id,customer_id,price_version_id,metric,usage_month,billing_month,total_units,
         exact_charge_ticks,booked_charge_cents,allocated_credit_ticks)
        VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8::numeric,$9,$10::numeric)
        ON CONFLICT (customer_id,price_version_id,usage_month,billing_month) DO UPDATE SET
        total_units=rated_usage_groups.total_units+EXCLUDED.total_units,
        exact_charge_ticks=rated_usage_groups.exact_charge_ticks+EXCLUDED.exact_charge_ticks,
        booked_charge_cents=$9,
        allocated_credit_ticks=rated_usage_groups.allocated_credit_ticks+EXCLUDED.allocated_credit_ticks`,
		group, e.CustomerID, rating.Price.ID, e.Metric, rating.UsageMonth, month, fmt.Sprint(e.Units),
		rating.Charge.Ticks().String(), cents, used.Ticks().String())
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, "INSERT INTO usage_ratings VALUES ($1,$2,$3)", e.Source, e.EventID, group)

	return err
}

func recordCreditDebit(ctx context.Context, tx pgx.Tx, e usage.Event, group string, used accounting.Amount) error {
	if used.Ticks().Sign() == 0 {
		return nil
	}

	operation := identity("usage/", e.Source, e.EventID)
	_, err := tx.Exec(ctx, `INSERT INTO credit_entries
        (credit_entry_id,customer_id,operation_id,group_id,amount_ticks,recorded_at)
        VALUES ($1,$2,$1,$3,$4::numeric,$5)`, operation, e.CustomerID, group,
		"-"+used.Ticks().String(), time.Now().UTC().Truncate(time.Microsecond))

	return err
}
