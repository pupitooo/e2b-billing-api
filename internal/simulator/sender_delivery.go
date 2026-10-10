package simulator

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"
)

type deliveryOutcome struct {
	Retry      bool
	RetryAfter time.Duration
	Err        error
}

// deliver owns retry and duplicate-copy scheduling for one immutable batch.
// Attempts and outcomes are checkpointed separately from transport inspection.
func (s *Sender) deliver(ctx context.Context, store *Store, state *State, body []byte, count int) error {
	delay := s.RetryMin
	successes := 0
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		outcome := s.attemptDelivery(ctx, store, state, body, count, attempt)
		if outcome.Err == nil {
			successes++
			if successes > s.Duplicates {
				return nil
			}

			s.log("Replaying an identical batch (%d/%d extra copies)\n", successes, s.Duplicates)
		} else if !outcome.Retry {
			return outcome.Err
		}

		if s.MaxAttempts > 0 && attempt >= s.MaxAttempts {
			return stopDeliveryAtLimit(store, state, s.MaxAttempts)
		}

		if outcome.Err != nil {
			var err error
			delay, err = s.waitForDeliveryRetry(ctx, delay, outcome.RetryAfter)
			if err != nil {
				return err
			}
		}
	}
}

// attemptDelivery coordinates one checkpointed request and optional response
// loss. Persistence failure is terminal even if the HTTP failure was retryable.
func (s *Sender) attemptDelivery(ctx context.Context, store *Store, state *State, body []byte, count, attempt int) deliveryOutcome {
	if err := store.checkpointSendAttempt(state); err != nil {
		return deliveryOutcome{Err: err}
	}

	s.log("Sending %d measurements; attempt=%d total_attempts=%d\n", count, attempt, state.Attempts)
	outcome := s.request(ctx, body)
	if outcome.Err == nil && s.LoseResponse && !state.LostResponseInjected {
		state.LostResponseInjected = true
		outcome.Retry = true
		outcome.Err = fmt.Errorf("simulated lost response after HTTP 202; retaining the same measurements")
	}

	if outcome.Err != nil {
		if err := store.checkpointDeliveryFailure(state, outcome.Err, "pending failure"); err != nil {
			return deliveryOutcome{Err: err}
		}

		s.log("Delivery failed: %v\n", outcome.Err)
	}

	return outcome
}

func stopDeliveryAtLimit(store *Store, state *State, limit int) error {
	failure := fmt.Errorf("reached max-attempts=%d; measurements remain pending", limit)
	if err := store.checkpointDeliveryFailure(state, failure, "attempt limit"); err != nil {
		return err
	}

	return failure
}

// waitForDeliveryRetry keeps server Retry-After as a lower bound, applies jitter
// to the client backoff, and allows cancellation throughout the wait.
func (s *Sender) waitForDeliveryRetry(ctx context.Context, delay, minimum time.Duration) (time.Duration, error) {
	jitter := time.Duration(rand.Int64N(max(1, int64(delay/4))))
	pause := max(min(delay+jitter, s.RetryMax), minimum)
	s.log("Retrying unchanged measurements in %s\n", pause)
	if err := s.sleep(ctx, pause); err != nil {
		return delay, err
	}

	return min(delay*2, s.RetryMax), nil
}
