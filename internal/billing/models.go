// Package billing defines financial inputs, validation, and invoice presentation.
package billing

import "time"

// PriceInput contains caller-owned price values; resource identities belong to billing.
// An empty customer denotes the default price. EffectiveFrom is midnight UTC.
type PriceInput struct {
	CustomerID           string    `json:"customer_id"`
	Metric               string    `json:"metric"`
	PricePerMillionCents int64     `json:"price_per_million_cents"`
	EffectiveFrom        time.Time `json:"effective_from"`
}

// CreditGrant contains caller values; the ledger audit time belongs to storage.
type CreditGrant struct {
	IdempotencyKey string `json:"idempotency_key"`
	AmountCents    int64  `json:"amount_cents"`
}

// CreditSnapshot reports exact balance and asynchronous accounting visibility.
// Counts describe committed receipts in the same database statement snapshot.
type CreditSnapshot struct {
	CustomerID        string `json:"customer_id"`
	BalanceMicrocents string `json:"credit_balance_microcents"`
	StateVersion      int64  `json:"state_version"`
	PendingEvents     int64  `json:"pending_events"`
	ProcessingErrors  int64  `json:"processing_errors"`
}

// AddonPurchase starts at midnight UTC on the caller-supplied calendar date.
type AddonPurchase struct {
	AddonName   string    `json:"addon_name"`
	PurchasedAt time.Time `json:"purchased_at"`
}

// AddonCancellation ends one purchase after its last fully charged UTC month.
type AddonCancellation struct {
	SubscriptionID string    `json:"subscription_id"`
	CancelledAt    time.Time `json:"cancelled_at"`
}

// Cancellation is an immutable termination record; EndMonth is inclusive.
type Cancellation struct {
	CustomerID     string    `json:"customer_id"`
	SubscriptionID string    `json:"subscription_id"`
	CancelledAt    time.Time `json:"cancelled_at"`
	EndMonth       string    `json:"end_month"`
}

type Subscription struct {
	SubscriptionID    string    `json:"subscription_id"`
	CustomerID        string    `json:"customer_id"`
	AddonName         string    `json:"addon_name"`
	MonthlyPriceCents int64     `json:"monthly_price_cents"`
	PurchasedAt       time.Time `json:"purchased_at"`
	StartMonth        string    `json:"start_month"`
}

type SpendLimitChange struct {
	IdempotencyKey string `json:"idempotency_key"`
	LimitCents     *int64 `json:"limit_cents"`
}

type LimitStatus struct {
	CustomerID            string `json:"customer_id"`
	Month                 string `json:"month"`
	LimitCents            *int64 `json:"limit_cents"`
	GrossChargeMicrocents string `json:"gross_charge_microcents"`
	LimitReached          bool   `json:"limit_reached"`
	StateVersion          int64  `json:"state_version"`
	PendingEvents         int64  `json:"pending_events"`
	ProcessingErrors      int64  `json:"processing_errors"`
}

type InvoiceLine struct {
	Kind             string `json:"kind"`
	Description      string `json:"description"`
	AmountCents      int64  `json:"amount_cents"`
	PriceVersionID   string `json:"price_version_id,omitempty"`
	Metric           string `json:"metric,omitempty"`
	UsageMonth       string `json:"usage_month,omitempty"`
	Units            string `json:"units,omitempty"`
	GrossMicrocents  string `json:"gross_microcents,omitempty"`
	CreditMicrocents string `json:"credit_microcents,omitempty"`
	SubscriptionID   string `json:"subscription_id,omitempty"`
}

// Invoice is an immutable buyer and financial snapshot, including exact audit
// microcents and the whole-cent presentation that sums to TotalCents.
type Invoice struct {
	CustomerID           string        `json:"customer_id"`
	Number               string        `json:"number"`
	Month                string        `json:"month"`
	CustomerName         string        `json:"customer_name"`
	Country              string        `json:"country"`
	BillingAddress       string        `json:"billing_address"`
	Currency             string        `json:"currency"`
	Lines                []InvoiceLine `json:"lines"`
	TotalCents           int64         `json:"total_cents"`
	GrossUsageMicrocents string        `json:"gross_usage_microcents"`
	CreditUsedMicrocents string        `json:"credit_used_microcents"`
	IssuedAt             time.Time     `json:"issued_at"`
}
