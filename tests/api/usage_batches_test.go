//go:build integration

package api_test

import (
	"encoding/json"
	"mime"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestUsageBatchesHappyPath sends the assignment's Acme measurement to a running
// API and verifies HTTP 202 with the accepted JSON response. The request and
// expectations remain the original contract; acceptance now follows inbox
// commit. Cleanup removes this fixture only when it did not exist beforehand.
func TestUsageBatchesHappyPath(t *testing.T) {
	preserveHappyPathFixture(t)
	apiURL := os.Getenv("E2B_API_URL")
	if apiURL == "" {
		apiURL = "http://127.0.0.1:8081"
	}

	const batch = `{
		"batch_id": "happy-path-acme-october",
		"events": [{
			"schema_version": 1,
			"source": "api-contract-test",
			"event_id": "acme-cpu-2026-10-10-12",
			"customer_id": "acme",
			"sandbox_id": "acme-sandbox-001",
			"metric": "cpu_seconds",
			"period_start": "2026-10-10T12:00:00Z",
			"period_end": "2026-10-10T13:00:00Z",
			"units": 100000000
		}]
	}`

	request, err := http.NewRequest(http.MethodPost, strings.TrimRight(apiURL, "/")+"/usage/batches", strings.NewReader(batch))
	if err != nil {
		t.Fatalf("Create usage request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("Send usage request: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("Status = %d, want %d", response.StatusCode, http.StatusAccepted)
	}

	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", response.Header.Get("Content-Type"))
	}

	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("Decode usage response: %v", err)
	}
	if body.Status != "accepted" {
		t.Errorf("Response status = %q, want accepted", body.Status)
	}
}
