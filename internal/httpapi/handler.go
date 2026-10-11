package httpapi

import (
	"errors"
	"net/http"
	"reflect"
	"time"

	"e2b/billing-api/internal/billingclock"
)

// NewHandler requires one store for every implemented endpoint. Missing storage
// is a startup error; route availability never depends on optional dependencies.
// The supplied business clock is shared with storage; nil selects real time.
func NewHandler(store Store, operationTimeout time.Duration, maxInFlight int, now func() time.Time) (http.Handler, error) {
	if missingStore(store) {
		return nil, errors.New("billing store is required")
	}

	if now == nil {
		now = billingclock.RealNow
	}

	mux := http.NewServeMux()
	registerUsageRoutes(mux, store, operationTimeout, maxInFlight, now)
	registerPriceRoutes(mux, store, operationTimeout)
	registerCreditRoutes(mux, store, operationTimeout)
	registerAddonRoutes(mux, store, operationTimeout)
	registerLimitRoutes(mux, store, operationTimeout, now)
	registerInvoiceRoutes(mux, store, operationTimeout)
	registerHealthRoutes(mux)

	return mux, nil
}

// missingStore also rejects a typed nil pointer or function inside an interface.
func missingStore(store Store) bool {
	if store == nil {
		return true
	}

	value := reflect.ValueOf(store)
	switch value.Kind() {
	case reflect.Pointer, reflect.Func, reflect.Map, reflect.Slice, reflect.Chan, reflect.Interface:
		return value.IsNil()
	default:
		return false
	}
}

func registerHealthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", getHealth)
}

func getHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}
