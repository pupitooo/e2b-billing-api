package simulator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const maxBatchBytes = 1_048_576

// Sender controls transport independently of the saved measurements.
type Sender struct {
	BaseURL      string
	Client       *http.Client
	BatchSize    int
	BatchDelay   time.Duration
	RetryMin     time.Duration
	RetryMax     time.Duration
	MaxAttempts  int
	Duplicates   int
	LoseResponse bool
	Reverse      bool
	Output       io.Writer
	wait         func(context.Context, time.Duration) error
}

func (s *Sender) validate() error {
	if !validBaseURL(s.BaseURL) {
		return fmt.Errorf("api-url must be an HTTP(S) base URL without credentials, query, or fragment")
	}
	if s.Client == nil || s.Client.Timeout <= 0 {
		return fmt.Errorf("an HTTP client with a positive timeout is required")
	}
	if s.BatchSize < 1 || s.BatchSize > 1_000 {
		return fmt.Errorf("batch-size must be between 1 and 1000")
	}
	if s.BatchDelay < 0 || s.RetryMin <= 0 || s.RetryMax < s.RetryMin || s.RetryMax > time.Hour {
		return fmt.Errorf("delay must be non-negative and retry delays must satisfy 0 < retry-min <= retry-max <= 1h")
	}
	if s.MaxAttempts < 0 || s.Duplicates < 0 || s.Duplicates > 10 {
		return fmt.Errorf("max-attempts must be non-negative and duplicates must be between 0 and 10")
	}
	return nil
}

func validBaseURL(value string) bool {
	address, err := url.Parse(value)
	if err != nil || (address.Scheme != "http" && address.Scheme != "https") {
		return false
	}
	return address.Host != "" && address.User == nil && address.RawQuery == "" && address.Fragment == ""
}

// Send drains released measurements. The receipt is saved only after every
// configured copy receives the documented acknowledgement; unknown outcomes
// leave identities and content pending for a safe retry after restart.
func (s *Sender) Send(ctx context.Context, store *Store, state *State, replay bool) error {
	if err := s.validate(); err != nil {
		return err
	}
	indices := state.pending(replay)
	if s.Reverse {
		slices.Reverse(indices)
	}
	events := state.Plan.events()
	for len(indices) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch, body, err := nextBatch(events, indices, s.BatchSize)
		if err != nil {
			return err
		}
		if err := s.deliver(ctx, store, state, body, len(batch)); err != nil {
			return err
		}
		previous := make([]bool, len(batch))
		for position, index := range batch {
			previous[position] = state.Delivered[index]
			state.Delivered[index] = true
		}
		state.LastError = ""
		if err := store.Save(state); err != nil {
			for position, index := range batch {
				state.Delivered[index] = previous[position]
			}
			state.LastError = "Delivery receipt could not be saved; retain and retry the original measurements."
			return fmt.Errorf("save delivery receipt: %w", err)
		}
		s.log("Delivered %d measurements; pending=%d\n", len(batch), len(state.pending(false)))
		indices = indices[len(batch):]
		if len(indices) > 0 {
			if err := s.sleep(ctx, s.BatchDelay); err != nil {
				return err
			}
		}
	}
	return nil
}

func nextBatch(events []Event, indices []int, size int) ([]int, []byte, error) {
	var items []Event
	length := len(`{"events":[]}`)
	for _, index := range indices[:min(size, len(indices))] {
		encoded, err := json.Marshal(events[index])
		if err != nil {
			return nil, nil, err
		}
		extra := len(encoded)
		if len(items) > 0 {
			extra++
		}
		if length+extra > maxBatchBytes {
			break
		}
		length += extra
		items = append(items, events[index])
	}
	if len(items) == 0 {
		return nil, nil, fmt.Errorf("a measurement exceeds the API body limit")
	}
	body, err := json.Marshal(struct {
		Events []Event `json:"events"`
	}{items})
	return indices[:len(items)], body, err
}

func (s *Sender) deliver(ctx context.Context, store *Store, state *State, body []byte, count int) error {
	delay := s.RetryMin
	successes := 0
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		state.Attempts++
		if err := store.Save(state); err != nil {
			return fmt.Errorf("save send attempt: %w", err)
		}
		s.log("Sending %d measurements; attempt=%d total_attempts=%d\n", count, attempt, state.Attempts)
		retry, minimum, err := s.request(ctx, body)
		if err == nil && s.LoseResponse && !state.LostResponseInjected {
			state.LostResponseInjected = true
			retry = true
			err = fmt.Errorf("simulated lost response after HTTP 202; retaining the same measurements")
		}
		if err == nil {
			successes++
			if successes > s.Duplicates {
				return nil
			}
			s.log("Replaying an identical batch (%d/%d extra copies)\n", successes, s.Duplicates)
		} else {
			state.LastError = err.Error()
			if saveErr := store.Save(state); saveErr != nil {
				return fmt.Errorf("save pending failure: %w", saveErr)
			}
			s.log("Delivery failed: %v\n", err)
			if !retry {
				return err
			}
		}
		if s.MaxAttempts > 0 && attempt >= s.MaxAttempts {
			err := fmt.Errorf("reached max-attempts=%d; measurements remain pending", s.MaxAttempts)
			state.LastError = err.Error()
			if saveErr := store.Save(state); saveErr != nil {
				return fmt.Errorf("save attempt limit: %w", saveErr)
			}
			return err
		}
		if err != nil {
			// Jitter never shortens Retry-After or the configured initial delay.
			jitter := time.Duration(rand.Int64N(max(1, int64(delay/4))))
			pause := max(min(delay+jitter, s.RetryMax), minimum)
			s.log("Retrying unchanged measurements in %s\n", pause)
			if err := s.sleep(ctx, pause); err != nil {
				return err
			}
			delay = min(delay*2, s.RetryMax)
		}
	}
}

func (s *Sender) request(ctx context.Context, body []byte) (bool, time.Duration, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(s.BaseURL, "/")+"/usage/batches", bytes.NewReader(body))
	if err != nil {
		return false, 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	// A redirect must not move measurements to a different endpoint or turn a
	// POST into GET. Inspect its response as a protocol error instead.
	client := *s.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return true, 0, fmt.Errorf("HTTP outcome unknown: %w", err)
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 65_537))
	if response.StatusCode == 429 || response.StatusCode >= 500 {
		return true, retryAfter(response.Header.Get("Retry-After")), fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(data[:min(len(data), 4_096)])))
	}
	if response.StatusCode != http.StatusAccepted {
		return false, 0, fmt.Errorf("HTTP %d requires investigation; measurements retained: %s", response.StatusCode, strings.TrimSpace(string(data[:min(len(data), 4_096)])))
	}
	if readErr != nil {
		return true, 0, fmt.Errorf("HTTP 202 body outcome unknown: %w", readErr)
	}
	var result struct {
		Status string `json:"status"`
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" || len(data) > 65_536 ||
		decodeStrict(bytes.NewReader(data), &result) != nil || result.Status != "accepted" {
		return false, 0, fmt.Errorf("HTTP 202 has an invalid acknowledgement; measurements retained")
	}
	return false, 0, nil
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

func (s *Sender) sleep(ctx context.Context, duration time.Duration) error {
	if s.wait != nil {
		return s.wait(ctx, duration)
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Sender) log(format string, values ...any) {
	if s.Output != nil {
		fmt.Fprintf(s.Output, format, values...)
	}
}
