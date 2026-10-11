package httpapi

import (
	"net/http"
	"time"
)

type invoiceHandler struct {
	store   Store
	timeout time.Duration
}

func registerInvoiceRoutes(mux *http.ServeMux, store Store, timeout time.Duration) {
	handler := invoiceHandler{store: store, timeout: timeout}

	mux.HandleFunc("POST /customers/{customer_id}/invoices", handler.closeMonth)
	mux.HandleFunc("GET /customers/{customer_id}/invoices/{month}", handler.getInvoice)
}

func (handler invoiceHandler) closeMonth(w http.ResponseWriter, r *http.Request) {
	requiredFields := []string{
		"month",
	}

	var input struct {
		Month string `json:"month"`
	}
	if !commandJSON(w, r, &input, requiredFields) {
		return
	}

	ctx, cancel := financialContext(r, handler.timeout)
	defer cancel()

	invoice, err := handler.store.CloseMonth(ctx, r.PathValue("customer_id"), input.Month)
	financialResult(w, invoice, err)
}

func (handler invoiceHandler) getInvoice(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := financialContext(r, handler.timeout)
	defer cancel()

	invoice, err := handler.store.Invoice(ctx, r.PathValue("customer_id"), r.PathValue("month"))
	financialResult(w, invoice, err)
}
