package httpapi

import (
	"net/http"
	"time"

	"e2b/billing-api/internal/billing"
)

func registerAddonRoutes(mux *http.ServeMux, store Store, timeout time.Duration) {
	mux.HandleFunc("POST /customers/{customer_id}/addons", func(w http.ResponseWriter, r *http.Request) {
		requiredFields := []string{
			"idempotency_key",
			"addon_name",
			"purchased_at",
		}

		var input struct {
			IdempotencyKey string `json:"idempotency_key"`
			AddonName      string `json:"addon_name"`
			PurchasedAt    string `json:"purchased_at"`
		}
		if !commandJSON(w, r, &input, requiredFields) {
			return
		}

		purchased, err := commandTime(input.PurchasedAt, "purchased_at")
		if err != nil {
			writeFinancialError(w, err)
			return
		}

		purchase := billing.AddonPurchase{
			AddonName:   input.AddonName,
			PurchasedAt: purchased,
		}

		ctx, cancel := financialContext(r, timeout)
		defer cancel()

		subscription, err := store.PurchaseAddon(ctx, r.PathValue("customer_id"), input.IdempotencyKey, purchase)
		financialResult(w, subscription, err)
	})
}
