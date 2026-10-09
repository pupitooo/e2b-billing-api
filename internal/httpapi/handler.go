package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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
		registerLimitRoutes(mux, financial[0], ingestionTimeout)
		registerInvoiceRoutes(mux, financial[0], ingestionTimeout)
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
	batch, validationError := readUsageBatch(w, r)
	if validationError != nil {
		writeRequestError(w, validationError)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), ingestionTimeout)
	defer cancel()
	if err := insertUsageBatch(ctx, store, batch.Events, receivedAt); err != nil {
		writeUsageStoreError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("{\"status\":\"accepted\"}\n"))
}

func readUsageBatch(w http.ResponseWriter, r *http.Request) (usageBatch, *requestError) {
	if err := usageRequestHeaders(r); err != nil {
		return usageBatch{}, err
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBatchBytes))
	if err != nil {
		var sizeError *http.MaxBytesError
		if errors.As(err, &sizeError) {
			return usageBatch{}, &requestError{status: http.StatusRequestEntityTooLarge, Code: "request_too_large", Message: "The request body must not exceed 1048576 bytes."}
		}
		return usageBatch{}, invalidJSON("Could not read the JSON request body.", "")
	}
	return parseUsageBatch(body)
}

func usageRequestHeaders(r *http.Request) *requestError {
	if !usesJSONUTF8(r) {
		return &requestError{status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type", Message: "Use application/json with UTF-8 encoding."}
	}
	if !usesIdentityEncoding(r) {
		return &requestError{status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type", Message: "Compressed request bodies are not supported."}
	}
	return nil
}

func insertUsageBatch(ctx context.Context, store BatchStore, events []usage.Event, receivedAt time.Time) error {
	if store == nil {
		return errors.New("inbox store is not configured")
	}
	return store.InsertBatch(ctx, events, receivedAt)
}

func writeUsageStoreError(w http.ResponseWriter, err error) {
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
