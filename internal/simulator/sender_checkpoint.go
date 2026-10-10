package simulator

import "fmt"

// checkpointSendAttempt persists the attempt before any request can commit at
// the API. Measurements and their delivery receipts stay pending at this point.
func (s *Store) checkpointSendAttempt(state *State) error {
	state.Attempts++
	if err := s.Save(state); err != nil {
		return fmt.Errorf("save send attempt: %w", err)
	}

	return nil
}

func (s *Store) checkpointDeliveryFailure(state *State, failure error, stage string) error {
	state.LastError = failure.Error()
	if err := s.Save(state); err != nil {
		return fmt.Errorf("save %s: %w", stage, err)
	}

	return nil
}

// checkpointDeliveryReceipt owns both the durable acknowledgement and the
// in-memory rollback. A failed save must remain visibly pending to the caller.
func (s *Store) checkpointDeliveryReceipt(state *State, batch []int) error {
	previous := make([]bool, len(batch))
	for position, index := range batch {
		previous[position] = state.Delivered[index]
		state.Delivered[index] = true
	}

	state.LastError = ""
	if err := s.Save(state); err != nil {
		for position, index := range batch {
			state.Delivered[index] = previous[position]
		}

		state.LastError = "Delivery receipt could not be saved; retain and retry the original measurements."

		return fmt.Errorf("save delivery receipt: %w", err)
	}

	return nil
}
