package httpapi

import (
	"net/http"
	"time"

	"e2b/billing-api/internal/billing"
)

func registerCreditRoutes(mux *http.ServeMux, store *billing.Store, timeout time.Duration) {
	mux.HandleFunc("POST /customers/{customer_id}/credits", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			IdempotencyKey string `json:"idempotency_key"`
			AmountCents    int64  `json:"amount_cents"`
			RecordedAt     string `json:"recorded_at"`
		}
		if !commandJSON(w, r, &input, []string{"idempotency_key", "amount_cents", "recorded_at"}) {
			return
		}

		recorded, err := commandTime(input.RecordedAt, "recorded_at")
		if err != nil {
			writeFinancialError(w, err)
			return
		}

		grant := billing.CreditGrant{IdempotencyKey: input.IdempotencyKey, AmountCents: input.AmountCents, RecordedAt: recorded}
		ctx, cancel := financialContext(r, timeout)
		defer cancel()
		err = store.GrantCredit(ctx, r.PathValue("customer_id"), grant)
		financialResult(w, grant, err)
	})
	mux.HandleFunc("GET /customers/{customer_id}/credit", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := financialContext(r, timeout)
		defer cancel()
		snapshot, err := store.Credit(ctx, r.PathValue("customer_id"))
		financialResult(w, snapshot, err)
	})
}
