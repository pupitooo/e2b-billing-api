package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"e2b/billing-api/internal/billing"
	"e2b/billing-api/internal/inbox"
	"e2b/billing-api/internal/usage"
)

// BatchStore returns success only after the whole batch is committed. It must
// preserve identical measurements and report changed content as ConflictError.
type BatchStore interface {
	InsertBatch(context.Context, []usage.Event, time.Time) error
}

// NewHandler routes requests through validation and the supplied durable store.
// The owner supplies positive database and admission budgets. Excess batches
// receive retryable 503 before application body parsing.
func NewHandler(store BatchStore, ingestionTimeout time.Duration, maxInFlight int, financial ...*billing.Store) http.Handler {
	mux := http.NewServeMux()
	if len(financial) == 1 && financial[0] != nil {
		registerPriceRoutes(mux, financial[0], ingestionTimeout)
		registerCreditRoutes(mux, financial[0], ingestionTimeout)
		registerAddonRoutes(mux, financial[0], ingestionTimeout)
	}
	inFlight := make(chan struct{}, maxInFlight)
	mux.HandleFunc("POST /usage/batches", func(w http.ResponseWriter, r *http.Request) {
		select {
		case inFlight <- struct{}{}:
			defer func() { <-inFlight }()
		default:
			writeInboxUnavailable(w)
			return
		}
		postUsageBatches(w, r, store, ingestionTimeout)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func postUsageBatches(w http.ResponseWriter, r *http.Request, store BatchStore, ingestionTimeout time.Duration) {
	receivedAt := time.Now().UTC().Truncate(time.Microsecond)
	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" ||
		(parameters["charset"] != "" && !strings.EqualFold(parameters["charset"], "utf-8")) {
		writeRequestError(w, &requestError{status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type", Message: "Use application/json with UTF-8 encoding."})
		return
	}
	if encoding := r.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		writeRequestError(w, &requestError{status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type", Message: "Compressed request bodies are not supported."})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBatchBytes))
	if err != nil {
		var sizeError *http.MaxBytesError
		if errors.As(err, &sizeError) {
			writeRequestError(w, &requestError{status: http.StatusRequestEntityTooLarge, Code: "request_too_large", Message: "The request body must not exceed 1048576 bytes."})
		} else {
			writeRequestError(w, &requestError{status: http.StatusBadRequest, Code: "invalid_json", Message: "Could not read the JSON request body."})
		}
		return
	}
	batch, validationError := parseUsageBatch(body)
	if validationError != nil {
		writeRequestError(w, validationError)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), ingestionTimeout)
	defer cancel()
	if store == nil {
		err = errors.New("inbox store is not configured")
	} else {
		err = store.InsertBatch(ctx, batch.Events, receivedAt)
	}
	if err != nil {
		var conflict *inbox.ConflictError
		if errors.As(err, &conflict) {
			writeRequestError(w, &requestError{
				status: http.StatusConflict, Code: "event_conflict",
				Message: "The event identity already has different measurement content.",
				Source:  conflict.Source, EventID: conflict.EventID,
			})
			return
		}
		// A failed/lost commit response can leave the outcome unknown. Clients
		// must retain the original keys and content when retrying after 503.
		writeInboxUnavailable(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("{\"status\":\"accepted\"}\n"))
}

// writeInboxUnavailable also covers admission exhaustion: no success is
// acknowledged, and unchanged producer retries preserve the event contract.
func writeInboxUnavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	writeRequestError(w, &requestError{
		status: http.StatusServiceUnavailable, Code: "inbox_unavailable",
		Message: "Inbox storage is unavailable. Retry the same event identities and content.",
	})
}

func writeRequestError(w http.ResponseWriter, err *requestError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(err.status)
	_ = json.NewEncoder(w).Encode(struct {
		Error *requestError `json:"error"`
	}{err})
}
