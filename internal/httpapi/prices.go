package httpapi

import (
	"net/http"
	"time"

	"e2b/billing-api/internal/accounting"
	"e2b/billing-api/internal/billing"
)

type priceCommand struct {
	ID                   string  `json:"price_version_id"`
	CustomerID           *string `json:"customer_id"`
	Metric               string  `json:"metric"`
	PricePerMillionCents int64   `json:"price_per_million_cents"`
	EffectiveFrom        string  `json:"effective_from"`
}

func registerPriceRoutes(mux *http.ServeMux, store *billing.Store, timeout time.Duration) {
	mux.HandleFunc("POST /prices", func(w http.ResponseWriter, r *http.Request) {
		var input priceCommand
		if !commandJSON(w, r, &input, []string{"price_version_id", "customer_id", "metric", "price_per_million_cents", "effective_from"}, "customer_id") {
			return
		}

		effective, err := commandTime(input.EffectiveFrom, "effective_from")
		if err != nil {
			writeFinancialError(w, err)
			return
		}

		price := accounting.PriceVersion{ID: input.ID, Metric: input.Metric, PricePerMillionCents: input.PricePerMillionCents, EffectiveFrom: effective}
		if input.CustomerID != nil {
			if err := billing.ValidateIdentifier("customer_id", *input.CustomerID); err != nil {
				writeFinancialError(w, err)
				return
			}

			price.CustomerID = *input.CustomerID
		}

		ctx, cancel := financialContext(r, timeout)
		defer cancel()
		err = store.CreatePrice(ctx, price)
		input.EffectiveFrom = effective.Format(time.RFC3339Nano)
		financialResult(w, input, err)
	})
}
