package simulator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
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
		if err := store.checkpointDeliveryReceipt(state, batch); err != nil {
			return err
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
