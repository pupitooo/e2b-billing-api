package httpapi

import (
	"net/http"
	"time"

	"e2b/billing-api/internal/billing"
)

func registerAddonRoutes(mux *http.ServeMux, store *billing.Store, timeout time.Duration) {
	mux.HandleFunc("POST /customers/{customer_id}/addons", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			SubscriptionID string `json:"subscription_id"`
			AddonName      string `json:"addon_name"`
			PurchasedAt    string `json:"purchased_at"`
		}
		if !commandJSON(w, r, &input, []string{"subscription_id", "addon_name", "purchased_at"}) {
			return
		}

		purchased, err := commandTime(input.PurchasedAt, "purchased_at")
		if err != nil {
			writeFinancialError(w, err)
			return
		}

		ctx, cancel := financialContext(r, timeout)
		defer cancel()
		subscription, err := store.PurchaseAddon(ctx, r.PathValue("customer_id"), billing.AddonPurchase{SubscriptionID: input.SubscriptionID, AddonName: input.AddonName, PurchasedAt: purchased})
		financialResult(w, subscription, err)
	})
}
