package billing

import (
	"time"

	"e2b/billing-api/internal/accounting"
)

// These storage facts deliberately contain no invoice presentation fields.
// Database decoding and financial presentation can change independently.
type InvoiceBuyer struct {
	Name           string
	Country        string
	BillingAddress string
	NextNumber     int64
}

type InvoiceUsage struct {
	PriceVersionID string
	Metric         string
	UsageMonth     time.Time
	Units          string
	Gross          accounting.Amount
	Credit         accounting.Amount
}

type InvoiceAddon struct {
	SubscriptionID string
	Name           string
	PriceCents     int64
}

type InvoiceCharges struct {
	Usage  []InvoiceUsage
	Addons []InvoiceAddon
}
