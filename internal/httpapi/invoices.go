package httpapi

import (
	"net/http"
	"time"
)

func registerInvoiceRoutes(mux *http.ServeMux, store Store, timeout time.Duration) {
	mux.HandleFunc("POST /customers/{customer_id}/invoices", func(w http.ResponseWriter, r *http.Request) {
		requiredFields := []string{
			"month",
		}

		var input struct {
			Month string `json:"month"`
		}
		if !commandJSON(w, r, &input, requiredFields) {
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
