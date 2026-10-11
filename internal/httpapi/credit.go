package httpapi

import (
	"net/http"
	"time"

	"e2b/billing-api/internal/billing"
)

type creditHandler struct {
	store   Store
	timeout time.Duration
}

func registerCreditRoutes(mux *http.ServeMux, store Store, timeout time.Duration) {
	handler := creditHandler{store: store, timeout: timeout}

	mux.HandleFunc("POST /customers/{customer_id}/credits", handler.grantCredit)
	mux.HandleFunc("GET /customers/{customer_id}/credit", handler.getCredit)
}

func (handler creditHandler) grantCredit(w http.ResponseWriter, r *http.Request) {
	requiredFields := []string{
		"idempotency_key",
		"amount_cents",
		"recorded_at",
	}

	var input struct {
		IdempotencyKey string `json:"idempotency_key"`
		AmountCents    int64  `json:"amount_cents"`
		RecordedAt     string `json:"recorded_at"`
	}
	if !commandJSON(w, r, &input, requiredFields) {
		return
	}

	recorded, err := commandTime(input.RecordedAt, "recorded_at")
	if err != nil {
		writeFinancialError(w, err)
		return
	}

	grant := billing.CreditGrant{
		IdempotencyKey: input.IdempotencyKey,
		AmountCents:    input.AmountCents,
		RecordedAt:     recorded,
	}

	ctx, cancel := financialContext(r, handler.timeout)
	defer cancel()

	err = handler.store.GrantCredit(ctx, r.PathValue("customer_id"), grant)
	financialResult(w, grant, err)
}

func (handler creditHandler) getCredit(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := financialContext(r, handler.timeout)
	defer cancel()

	snapshot, err := handler.store.Credit(ctx, r.PathValue("customer_id"))
	financialResult(w, snapshot, err)
}
