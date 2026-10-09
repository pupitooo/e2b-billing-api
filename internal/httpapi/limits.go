package httpapi

import (
	"net/http"
	"time"

	"e2b/billing-api/internal/billing"
)

func registerLimitRoutes(mux *http.ServeMux, store *billing.Store, timeout time.Duration) {
	mux.HandleFunc("POST /customers/{customer_id}/spend-limit", func(w http.ResponseWriter, r *http.Request) {
		var change billing.SpendLimitChange
		if !commandJSON(w, r, &change, []string{"operation_id", "limit_cents"}, "limit_cents") {
			return
		}
		ctx, cancel := financialContext(r, timeout)
		defer cancel()
		err := store.SetSpendLimit(ctx, r.PathValue("customer_id"), change)
		financialResult(w, change, err)
	})
	status := func(w http.ResponseWriter, r *http.Request) {
		month := r.PathValue("month")
		if month == "" {
			month = time.Now().UTC().Format("2006-01")
		}
		ctx, cancel := financialContext(r, timeout)
		defer cancel()
		result, err := store.MonthlyLimitStatus(ctx, r.PathValue("customer_id"), month)
		financialResult(w, result, err)
	}
	mux.HandleFunc("GET /customers/{customer_id}/limit-status", status)
	mux.HandleFunc("GET /customers/{customer_id}/months/{month}/limit-status", status)
}
