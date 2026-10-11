package httpapi

import (
	"context"
	"time"

	"e2b/billing-api/internal/accounting"
	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/usage"
)

// BatchStore commits complete usage batches and preserves identical retries.
type BatchStore interface {
	InsertBatch(context.Context, []usage.Event, time.Time) error
}

// Store supplies all persistence operations required by the public HTTP API.
// PostgreSQL implements this contract using one pool and shared configuration.
type Store interface {
	BatchStore
	CreatePrice(context.Context, string, billing.PriceInput) (accounting.PriceVersion, error)
	GrantCredit(context.Context, string, billing.CreditGrant) error
	Credit(context.Context, string) (billing.CreditSnapshot, error)
	PurchaseAddon(context.Context, string, string, billing.AddonPurchase) (billing.Subscription, error)
	CancelAddon(context.Context, string, string, billing.AddonCancellation) (billing.Cancellation, error)
	SetSpendLimit(context.Context, string, billing.SpendLimitChange) error
	MonthlyLimitStatus(context.Context, string, string) (billing.LimitStatus, error)
	CloseMonth(context.Context, string, string) (billing.Invoice, error)
	Invoice(context.Context, string, string) (billing.Invoice, error)
}
