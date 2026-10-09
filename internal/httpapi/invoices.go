package httpapi

import (
	"net/http"
	"time"

	"e2b/billing-api/internal/billing"
)

func registerInvoiceRoutes(mux *http.ServeMux, store *billing.Store, timeout time.Duration) {
	mux.HandleFunc("POST /customers/{customer_id}/invoices", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Month string `json:"month"`
		}
		if !commandJSON(w, r, &input, []string{"month"}) {
			return
		}
		ctx, cancel := financialContext(r, timeout)
		defer cancel()
		invoice, err := store.CloseMonth(ctx, r.PathValue("customer_id"), input.Month)
		financialResult(w, invoice, err)
	})
	mux.HandleFunc("GET /customers/{customer_id}/invoices/{month}", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := financialContext(r, timeout)
		defer cancel()
		invoice, err := store.Invoice(ctx, r.PathValue("customer_id"), r.PathValue("month"))
		financialResult(w, invoice, err)
	})
}
