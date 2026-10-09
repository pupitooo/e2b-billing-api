package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"e2b/billing-api/internal/httpapi"
)

func TestHandlerRoutes(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		status int
		allow  string
	}{
		{"health", http.MethodGet, "/healthz", http.StatusOK, ""},
		{"usage batch", http.MethodPost, "/usage/batches", http.StatusAccepted, ""},
		{"unknown route", http.MethodGet, "/missing", http.StatusNotFound, ""},
		{"usage method", http.MethodGet, "/usage/batches", http.StatusMethodNotAllowed, "POST"},
	}
	handler := httpapi.NewHandler()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(tt.method, tt.path, nil))
			if response.Code != tt.status {
				t.Fatalf("Status = %d, want %d", response.Code, tt.status)
			}
			if tt.allow != "" && response.Header().Get("Allow") != tt.allow {
				t.Errorf("Allow = %q, want %q", response.Header().Get("Allow"), tt.allow)
			}
		})
	}
}

func TestUsageBatchResponse(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/usage/batches", strings.NewReader(`{"batch_id":"test","events":[]}`))
	request.Header.Set("Content-Type", "application/json")
	httpapi.NewHandler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("Status = %d, want %d", response.Code, http.StatusAccepted)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("Decode response: %v", err)
	}
	if body.Status != "accepted" {
		t.Errorf("Response status = %q, want accepted", body.Status)
	}
}
