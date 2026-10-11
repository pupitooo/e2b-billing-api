package httpapi

import (
	"net/http"
	"time"

	"e2b/billing-api/internal/billing"
)

type limitHandler struct {
	now     func() time.Time
	store   Store
	timeout time.Duration
}

func registerLimitRoutes(mux *http.ServeMux, store Store, timeout time.Duration, now func() time.Time) {
	handler := limitHandler{store: store, timeout: timeout, now: now}

	mux.HandleFunc("POST /customers/{customer_id}/spend-limit", handler.setSpendLimit)
	mux.HandleFunc("GET /customers/{customer_id}/limit-status", handler.getLimitStatus)
	mux.HandleFunc("GET /customers/{customer_id}/months/{month}/limit-status", handler.getLimitStatus)
}

func (handler limitHandler) setSpendLimit(w http.ResponseWriter, r *http.Request) {
	requiredFields := []string{
		"idempotency_key",
		"limit_cents",
	}
	nullableFields := []string{
		"limit_cents",
	}

	var change billing.SpendLimitChange
	if !commandJSON(w, r, &change, requiredFields, nullableFields...) {
		return
	}

	ctx, cancel := financialContext(r, handler.timeout)
	defer cancel()

	err := handler.store.SetSpendLimit(ctx, r.PathValue("customer_id"), change)
	financialResult(w, change, err)
}

func (handler limitHandler) getLimitStatus(w http.ResponseWriter, r *http.Request) {
	month := r.PathValue("month")
	if month == "" {
		month = handler.now().UTC().Format("2006-01")
	}

	ctx, cancel := financialContext(r, handler.timeout)
	defer cancel()

	result, err := handler.store.MonthlyLimitStatus(ctx, r.PathValue("customer_id"), month)
	financialResult(w, result, err)
}
