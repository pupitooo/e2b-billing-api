package httpapi

import "net/http"

func NewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /usage/batches", postUsageBatches)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func postUsageBatches(w http.ResponseWriter, _ *http.Request) {
	// TODO: Validate and persist the batch before acknowledging receipt.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("{\"status\":\"accepted\"}\n"))
}
