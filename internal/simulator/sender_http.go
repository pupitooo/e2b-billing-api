package simulator

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// request performs exactly one exchange. HTTP acknowledgement classification
// does not change checkpoints or decide how many times a batch will be sent.
func (s *Sender) request(ctx context.Context, body []byte) deliveryOutcome {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(s.BaseURL, "/")+"/usage/batches", bytes.NewReader(body))
	if err != nil {
		return deliveryOutcome{Err: err}
	}
	request.Header.Set("Content-Type", "application/json")
	// A redirect must not move measurements to a different endpoint or turn a
	// POST into GET. Inspect its response as a protocol error instead.
	client := *s.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return deliveryOutcome{Retry: true, Err: fmt.Errorf("HTTP outcome unknown: %w", err)}
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 65_537))
	return classifyDeliveryResponse(response, data, readErr)
}

func classifyDeliveryResponse(response *http.Response, data []byte, readErr error) deliveryOutcome {
	if response.StatusCode == 429 || response.StatusCode >= 500 {
		return deliveryOutcome{
			Retry: true, RetryAfter: retryAfter(response.Header.Get("Retry-After")),
			Err: fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(data[:min(len(data), 4_096)]))),
		}
	}
	if response.StatusCode != http.StatusAccepted {
		return deliveryOutcome{Err: fmt.Errorf("HTTP %d requires investigation; measurements retained: %s", response.StatusCode, strings.TrimSpace(string(data[:min(len(data), 4_096)])))}
	}
	if readErr != nil {
		return deliveryOutcome{Retry: true, Err: fmt.Errorf("HTTP 202 body outcome unknown: %w", readErr)}
	}
	var result struct {
		Status string `json:"status"`
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" || len(data) > 65_536 ||
		decodeStrict(bytes.NewReader(data), &result) != nil || result.Status != "accepted" {
		return deliveryOutcome{Err: fmt.Errorf("HTTP 202 has an invalid acknowledgement; measurements retained")}
	}
	return deliveryOutcome{}
}

func retryAfter(value string) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
		// Saturate instead of overflowing; the wait remains cancelable.
		return time.Duration(min(seconds, int64((1<<63-1)/time.Second))) * time.Second
	}
	if instant, err := http.ParseTime(value); err == nil {
		return max(0, time.Until(instant))
	}
	return 0
}
