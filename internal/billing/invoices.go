package billing

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type InvoiceLine struct {
	Kind           string `json:"kind"`
	Description    string `json:"description"`
	AmountCents    int64  `json:"amount_cents"`
	PriceVersionID string `json:"price_version_id,omitempty"`
	Metric         string `json:"metric,omitempty"`
	UsageMonth     string `json:"usage_month,omitempty"`
	Units          string `json:"units,omitempty"`
	GrossTicks     string `json:"gross_ticks,omitempty"`
	CreditTicks    string `json:"credit_ticks,omitempty"`
	SubscriptionID string `json:"subscription_id,omitempty"`
}

// Invoice is an immutable buyer and financial snapshot, including exact audit
// ticks and the whole-cent presentation that sums to TotalCents.
type Invoice struct {
	CustomerID      string        `json:"customer_id"`
	Number          string        `json:"number"`
	Month           string        `json:"month"`
	CustomerName    string        `json:"customer_name"`
	Country         string        `json:"country"`
	BillingAddress  string        `json:"billing_address"`
	Currency        string        `json:"currency"`
	Lines           []InvoiceLine `json:"lines"`
	TotalCents      int64         `json:"total_cents"`
	GrossUsageTicks string        `json:"gross_usage_ticks"`
	CreditUsedTicks string        `json:"credit_used_ticks"`
	IssuedAt        time.Time     `json:"issued_at"`
}

// CloseMonth durably captures a cohort, drains it without an account lock held
// across transactions, then freezes the result and allocates one invoice number.
// A timeout leaves resumable work; cohort errors block issuance visibly.
func (s *Store) CloseMonth(ctx context.Context, customer, monthValue string) (Invoice, error) {
	if err := ValidateIdentifier("customer_id", customer); err != nil {
		return Invoice{}, err
	}
	month, err := ParseMonth(monthValue)
	if err != nil {
		return Invoice{}, err
	}
	if err := s.beginClosing(ctx, customer, month); err != nil {
		return Invoice{}, err
	}
	if err := s.drainClosing(ctx, customer, month); err != nil {
		return Invoice{}, err
	}
	var result Invoice
	err = s.transact(ctx, func(tx pgx.Tx) error {
		if _, err := lockAccount(ctx, tx, customer); err != nil {
			return err
		}
		previous, err := invoiceInTransaction(ctx, tx, customer, month)
		if err == nil {
			result = previous
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := closingReady(ctx, tx, customer, month); err != nil {
			return err
		}
		result, err = buildInvoice(ctx, tx, customer, month)
		if err != nil {
			return err
		}
		return freezeInvoice(ctx, tx, result, month)
	})
	return result, err
}

func (s *Store) Invoice(ctx context.Context, customer, monthValue string) (Invoice, error) {
	if err := ValidateIdentifier("customer_id", customer); err != nil {
		return Invoice{}, err
	}
	month, err := ParseMonth(monthValue)
	if err != nil {
		return Invoice{}, err
	}
	var encoded []byte
	err = s.pool.QueryRow(ctx, "SELECT snapshot FROM invoices WHERE customer_id=$1 AND billing_month=$2", customer, month).Scan(&encoded)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invoice{}, ErrNotFound
	}
	if err != nil {
		return Invoice{}, err
	}
	var result Invoice
	err = json.Unmarshal(encoded, &result)
	return result, err
}

func invoiceInTransaction(ctx context.Context, tx pgx.Tx, customer string, month time.Time) (Invoice, error) {
	var encoded []byte
	err := tx.QueryRow(ctx, "SELECT snapshot FROM invoices WHERE customer_id=$1 AND billing_month=$2", customer, month).Scan(&encoded)
	if err != nil {
		return Invoice{}, err
	}
	var result Invoice
	err = json.Unmarshal(encoded, &result)
	return result, err
}
