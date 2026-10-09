// Package simulator provides the separately deployed platform stand-in.
package simulator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"e2b/billing-api/internal/usage"
)

// Event is the explicit HTTP measurement, without billing-owned metadata.
type Event struct {
	Source        string    `json:"source"`
	EventID       string    `json:"event_id"`
	SchemaVersion int32     `json:"schema_version"`
	CustomerID    string    `json:"customer_id"`
	SandboxID     string    `json:"sandbox_id"`
	Metric        string    `json:"metric"`
	PeriodStart   time.Time `json:"period_start"`
	PeriodEnd     time.Time `json:"period_end"`
	Units         int64     `json:"units"`
}

func (e Event) validate() error {
	event := usage.Event{
		Source: e.Source, EventID: e.EventID, SchemaVersion: e.SchemaVersion,
		CustomerID: e.CustomerID, SandboxID: e.SandboxID, Metric: e.Metric,
		PeriodStart: e.PeriodStart, PeriodEnd: e.PeriodEnd, Units: e.Units,
	}
	if err := event.Validate(); err != nil {
		return fmt.Errorf("event %q: %w", e.EventID, err)
	}
	for _, value := range []string{e.Source, e.EventID, e.CustomerID, e.SandboxID, e.Metric} {
		if len(value) > 256 {
			return fmt.Errorf("event %q: identifiers must fit in 256 UTF-8 bytes", e.EventID)
		}
	}
	return nil
}

func decodeStrict(reader io.Reader, target any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON document")
	}
	return nil
}

func sameJSON(left, right any) bool {
	a, _ := json.Marshal(left)
	b, _ := json.Marshal(right)
	return bytes.Equal(a, b)
}
