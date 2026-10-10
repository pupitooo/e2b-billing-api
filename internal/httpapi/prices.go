package httpapi

import (
	"net/http"
	"time"

	"e2b/billing-api/internal/accounting"
	"e2b/billing-api/internal/billing"
)

type priceValues struct {
	CustomerID           *string `json:"customer_id"`
	Metric               string  `json:"metric"`
	PricePerMillionCents int64   `json:"price_per_million_cents"`
	EffectiveFrom        *string `json:"effective_from"`
}

type priceCommand struct {
	IdempotencyKey string `json:"idempotency_key"`
	priceValues
}

type priceResponse struct {
	ID string `json:"price_version_id"`
	priceValues
}

func registerPriceRoutes(mux *http.ServeMux, store *billing.Store, timeout time.Duration) {
	mux.HandleFunc("POST /prices", func(w http.ResponseWriter, r *http.Request) {
		var input priceCommand
		if !commandJSON(w, r, &input, []string{"idempotency_key", "customer_id", "metric", "price_per_million_cents", "effective_from"}, "customer_id", "effective_from") {
			return
		}

		var effective time.Time
		if input.EffectiveFrom != nil {
			var err error
			effective, err = commandTime(*input.EffectiveFrom, "effective_from")
			if err != nil {
				writeFinancialError(w, err)
				return
			}
		}

		command := billing.PriceInput{Metric: input.Metric, PricePerMillionCents: input.PricePerMillionCents, EffectiveFrom: effective}
		if input.CustomerID != nil {
			if err := billing.ValidateIdentifier("customer_id", *input.CustomerID); err != nil {
				writeFinancialError(w, err)
				return
			}

			command.CustomerID = *input.CustomerID
		}

		ctx, cancel := financialContext(r, timeout)
		defer cancel()
		var err error
		var price accounting.PriceVersion
		if input.EffectiveFrom == nil {
			price, err = store.CreatePriceNow(ctx, input.IdempotencyKey, command)
		} else {
			price, err = store.CreatePrice(ctx, input.IdempotencyKey, command)
		}

		resolved := price.EffectiveFrom.Format(time.RFC3339Nano)
		input.EffectiveFrom = &resolved
		financialResult(w, priceResponse{ID: price.ID, priceValues: input.priceValues}, err)
	})
}
