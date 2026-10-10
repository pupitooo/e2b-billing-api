package simulator

import (
	"fmt"
	"io"
	"strings"
	"time"
)

const maxScenarioEvents = 10_000

// Step releases a group of measurements. A barrier requires operator consent
// before release; the simulator does not itself issue or inspect invoices.
type Step struct {
	Name    string  `json:"name"`
	Barrier string  `json:"barrier,omitempty"`
	Events  []Event `json:"events"`
}

// Plan is saved with the sender state before any measurement can be sent.
type Plan struct {
	Name  string `json:"name"`
	Steps []Step `json:"steps"`
}

// Assignment splits the assignment's six hourly totals into exact integer
// increments while retaining consumption times and deterministic identities.
func Assignment(source string, sandboxes int, interval time.Duration) (Plan, error) {
	if sandboxes < 1 || sandboxes > 1_000 {
		return Plan{}, fmt.Errorf("sandboxes must be between 1 and 1000")
	}

	if interval < time.Minute || interval > time.Hour || time.Hour%interval != 0 {
		return Plan{}, fmt.Errorf("interval must divide one hour and be between 1m and 1h")
	}

	segments := int(time.Hour / interval)
	if 6*sandboxes*segments > maxScenarioEvents {
		return Plan{}, fmt.Errorf("the scenario must contain at most %d events", maxScenarioEvents)
	}

	plan := Plan{Name: "assignment"}
	for _, input := range assignmentSteps() {
		step := Step{Name: input.name, Barrier: input.barrier}
		for _, hour := range input.hours {
			events, err := splitHour(source, hour, sandboxes, interval)
			if err != nil {
				return Plan{}, err
			}

			step.Events = append(step.Events, events...)
		}

		plan.Steps = append(plan.Steps, step)
	}

	return plan, plan.validate()
}

type hourInput struct {
	customer string
	start    string
	units    int64
}

type assignmentStep struct {
	name    string
	barrier string
	hours   []hourInput
}

// assignmentSteps keeps the binding measurements with their named business phase.
func assignmentSteps() []assignmentStep {
	return []assignmentStep{
		{name: "october-first", hours: []hourInput{
			{customer: "acme", start: "2026-10-10T12:00:00Z", units: 100_000_000},
			{customer: "cyberdyne", start: "2026-10-10T12:00:00Z", units: 123_456_789},
		}},
		{name: "october-second", hours: []hourInput{
			{customer: "acme", start: "2026-10-20T12:00:00Z", units: 200_000_000},
			{customer: "cyberdyne", start: "2026-10-20T12:00:00Z", units: 200_000_000},
		}},
		{name: "late-october", barrier: "Issue the October invoices after accounting completes; then use ADVANCE=1.", hours: []hourInput{
			{customer: "acme", start: "2026-10-30T12:00:00Z", units: 50_000_000},
		}},
		{name: "november", hours: []hourInput{
			{customer: "acme", start: "2026-11-03T12:00:00Z", units: 100_000_000},
		}},
	}
}

// splitHour distributes integer units with a visible remainder rule. It retains
// the previous event ordering and identity scheme for durable saved plans.
func splitHour(source string, input hourInput, sandboxes int, interval time.Duration) ([]Event, error) {
	start, err := time.Parse(time.RFC3339, input.start)
	if err != nil {
		return nil, err
	}

	segments := int(time.Hour / interval)
	count := sandboxes * segments
	events := make([]Event, 0, count)
	for sandbox := range sandboxes {
		for segment := range segments {
			index := sandbox*segments + segment
			units := input.units / int64(count)
			if int64(index) < input.units%int64(count) {
				units++
			}

			period := start.Add(time.Duration(segment) * interval)
			events = append(events, Event{
				Source:        source,
				EventID:       fmt.Sprintf("assignment-%s-%s-s%04d-p%04d", input.customer, start.Format("2006-01-02"), sandbox+1, segment+1),
				SchemaVersion: 1, CustomerID: input.customer,
				SandboxID: fmt.Sprintf("%s-sandbox-%04d", input.customer, sandbox+1),
				Metric:    "cpu_seconds", PeriodStart: period, PeriodEnd: period.Add(interval), Units: units,
			})
		}
	}

	return events, nil
}

// Custom loads an operator-defined scenario. Source belongs to the run, while
// every other event field must be explicitly supplied, including zero units.
func Custom(reader io.Reader, source string) (Plan, error) {
	var input struct {
		Name  string `json:"name"`
		Steps []struct {
			Name    string `json:"name"`
			Barrier string `json:"barrier,omitempty"`
			Events  []struct {
				EventID       *string    `json:"event_id"`
				SchemaVersion *int32     `json:"schema_version"`
				CustomerID    *string    `json:"customer_id"`
				SandboxID     *string    `json:"sandbox_id"`
				Metric        *string    `json:"metric"`
				PeriodStart   *time.Time `json:"period_start"`
				PeriodEnd     *time.Time `json:"period_end"`
				Units         *int64     `json:"units"`
			} `json:"events"`
		} `json:"steps"`
	}
	if err := decodeStrict(io.LimitReader(reader, 16<<20), &input); err != nil {
		return Plan{}, fmt.Errorf("decode scenario: %w", err)
	}

	plan := Plan{Name: input.Name}
	for stepIndex, inputStep := range input.Steps {
		step := Step{Name: inputStep.Name, Barrier: inputStep.Barrier}
		for eventIndex, item := range inputStep.Events {
			if item.EventID == nil || item.SchemaVersion == nil || item.CustomerID == nil ||
				item.SandboxID == nil || item.Metric == nil || item.PeriodStart == nil ||
				item.PeriodEnd == nil || item.Units == nil {
				return Plan{}, fmt.Errorf("step %d event %d: every event field is required and must be non-null", stepIndex, eventIndex)
			}

			step.Events = append(step.Events, Event{
				Source: source, EventID: *item.EventID, SchemaVersion: *item.SchemaVersion,
				CustomerID: *item.CustomerID, SandboxID: *item.SandboxID, Metric: *item.Metric,
				PeriodStart: item.PeriodStart.UTC(), PeriodEnd: item.PeriodEnd.UTC(), Units: *item.Units,
			})
		}

		plan.Steps = append(plan.Steps, step)
	}

	return plan, plan.validate()
}

func (p Plan) validate() error {
	if strings.TrimSpace(p.Name) == "" || len(p.Steps) == 0 {
		return fmt.Errorf("a scenario needs a name and at least one step")
	}

	identities := make(map[[2]string]bool)
	stepNames := make(map[string]bool)
	for _, step := range p.Steps {
		if strings.TrimSpace(step.Name) == "" || stepNames[step.Name] || len(step.Events) == 0 {
			return fmt.Errorf("steps need unique nonblank names and at least one event")
		}

		stepNames[step.Name] = true
		for _, event := range step.Events {
			if err := event.validate(); err != nil {
				return err
			}

			key := [2]string{event.Source, event.EventID}
			if identities[key] {
				return fmt.Errorf("duplicate event identity %q/%q; use replay or duplicates to test retries", event.Source, event.EventID)
			}

			identities[key] = true
			if len(identities) > maxScenarioEvents {
				return fmt.Errorf("the scenario must contain at most %d events", maxScenarioEvents)
			}
		}
	}

	return nil
}

func (p Plan) events() []Event {
	var events []Event
	for _, step := range p.Steps {
		events = append(events, step.Events...)
	}

	return events
}
