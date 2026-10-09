package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

func NewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /usage/batches", postUsageBatches)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func postUsageBatches(w http.ResponseWriter, r *http.Request) {
	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" ||
		(parameters["charset"] != "" && !strings.EqualFold(parameters["charset"], "utf-8")) {
		writeRequestError(w, &requestError{http.StatusUnsupportedMediaType, "unsupported_media_type", "Use application/json with UTF-8 encoding.", ""})
		return
	}
	if encoding := r.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		writeRequestError(w, &requestError{http.StatusUnsupportedMediaType, "unsupported_media_type", "Compressed request bodies are not supported.", ""})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBatchBytes))
	if err != nil {
		var sizeError *http.MaxBytesError
		if errors.As(err, &sizeError) {
			writeRequestError(w, &requestError{http.StatusRequestEntityTooLarge, "request_too_large", "The request body must not exceed 1048576 bytes.", ""})
		} else {
			writeRequestError(w, &requestError{http.StatusBadRequest, "invalid_json", "Could not read the JSON request body.", ""})
		}
		return
	}
	if _, err := parseUsageBatch(body); err != nil {
		writeRequestError(w, err)
		return
	}
	// Response stub: validation is implemented, durable acceptance follows later.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("{\"status\":\"accepted\"}\n"))
}

func writeRequestError(w http.ResponseWriter, err *requestError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(err.status)
	_ = json.NewEncoder(w).Encode(struct {
		Error *requestError `json:"error"`
	}{err})
}
