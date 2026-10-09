package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"e2b/billing-api/internal/httpapi"
)

// TestHandlerRoutes verifies routing, response codes, and allowed methods.
func TestHandlerRoutes(t *testing.T) {
	tests := []struct {
		name        string
		description string
		method      string
		path        string
		status      int
		allow       string
	}{
		{"health", "The health endpoint responds successfully to GET requests.", http.MethodGet, "/healthz", http.StatusOK, ""},
		{"usage batch", "The usage endpoint acknowledges POST requests with HTTP 202.", http.MethodPost, "/usage/batches", http.StatusAccepted, ""},
		{"unknown route", "An unregistered path returns HTTP 404.", http.MethodGet, "/missing", http.StatusNotFound, ""},
		{"usage method", "The usage endpoint rejects GET requests and advertises POST in the Allow header.", http.MethodGet, "/usage/batches", http.StatusMethodNotAllowed, "POST"},
	}
	handler := httpapi.NewHandler()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log(tt.description)
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

// TestUsageBatchResponse verifies the current acknowledgement response:
// HTTP 202, a JSON content type, and an "accepted" status. Validation and
// persistence of the request body are not implemented yet.
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
