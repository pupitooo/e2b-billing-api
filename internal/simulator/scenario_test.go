package simulator

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestSimulatorAssignmentTotalsAndSplitting verifies the binding six hourly
// totals, including the indivisible Cyberdyne value, across hourly and minute
// sandbox splits. Rebuilding the plan must preserve identities and content.
func TestSimulatorAssignmentTotalsAndSplitting(t *testing.T) {
	want := map[string]int64{
		"acme/2026-10-10": 100_000_000, "cyberdyne/2026-10-10": 123_456_789,
		"acme/2026-10-20": 200_000_000, "cyberdyne/2026-10-20": 200_000_000,
		"acme/2026-10-30": 50_000_000, "acme/2026-11-03": 100_000_000,
	}
	for _, interval := range []time.Duration{time.Hour, time.Minute} {
		t.Run(interval.String(), func(t *testing.T) {
			plan, err := Assignment("test-source", 3, interval)
			if err != nil {
				t.Fatal(err)
			}
			again, err := Assignment("test-source", 3, interval)
			if err != nil || !sameJSON(plan, again) {
				t.Fatalf("Plan is not deterministic: %v", err)
			}
			if count := len(plan.events()); count != 6*3*int(time.Hour/interval) {
				t.Fatalf("Event count = %d", count)
			}
			totals := make(map[string]int64)
			previous := make(map[string]time.Time)
			for _, event := range plan.events() {
				key := event.CustomerID + "/" + event.PeriodStart.Format("2006-01-02")
				totals[key] += event.Units
				if event.Units < 0 || event.PeriodStart.Location() != time.UTC || event.PeriodEnd.Sub(event.PeriodStart) != interval {
					t.Errorf("Invalid measurement split: %+v", event)
				}
				sandbox := key + "/" + event.SandboxID
				if end, present := previous[sandbox]; present && !end.Equal(event.PeriodStart) {
					t.Errorf("Intervals do not meet for %s", sandbox)
				}
				previous[sandbox] = event.PeriodEnd
			}
			for key, units := range want {
				if totals[key] != units {
					t.Errorf("%s = %d, want %d", key, totals[key], units)
				}
			}
			if len(totals) != len(want) || plan.Steps[2].Barrier == "" {
				t.Error("Unexpected fixture groups or missing late-October barrier")
			}
		})
	}
}

// TestSimulatorAssignmentBounds refuses unsupported splitting before creating
// a large schedule, while an exact non-minute divisor remains a valid interval.
func TestSimulatorAssignmentBounds(t *testing.T) {
	for _, configuration := range []struct {
		sandboxes int
		interval  time.Duration
	}{{0, time.Hour}, {1_001, time.Hour}, {1, 59 * time.Second}, {1, 7 * time.Minute}, {1_000, time.Minute}} {
		if _, err := Assignment("test", configuration.sandboxes, configuration.interval); err == nil {
			t.Errorf("Accepted unsupported split %+v", configuration)
		}
	}
	if _, err := Assignment("test", 2, 90*time.Second); err != nil {
		t.Errorf("Rejected exact interval divisor: %v", err)
	}
}

// TestSimulatorCustomExplicitFields distinguishes explicit zero from missing
// or null units and rejects invalid or ambiguous scenario identities before
// delivery. UTC normalization must retain the supplied consumption instant.
func TestSimulatorCustomExplicitFields(t *testing.T) {
	const event = `{"event_id":"one","schema_version":1,"customer_id":"acme","sandbox_id":"one","metric":"cpu_seconds","period_start":"2026-10-10T20:00:00+08:00","period_end":"2026-10-10T20:01:00+08:00","units":0}`
	input := fmt.Sprintf(`{"name":"custom","steps":[{"name":"first","events":[%s]}]}`, event)
	plan, err := Custom(strings.NewReader(input), "custom-source")
	if err != nil {
		t.Fatal(err)
	}
	got := plan.events()[0]
	if got.Source != "custom-source" || got.Units != 0 || got.PeriodStart.Format(time.RFC3339) != "2026-10-10T12:00:00Z" {
		t.Errorf("Custom measurement = %+v", got)
	}
	for name, invalid := range map[string]string{
		"missing units":      strings.Replace(input, `,"units":0`, "", 1),
		"null units":         strings.Replace(input, `"units":0`, `"units":null`, 1),
		"negative units":     strings.Replace(input, `"units":0`, `"units":-1`, 1),
		"unknown field":      strings.Replace(input, `"units":0`, `"units":0,"cost":10`, 1),
		"duplicate identity": fmt.Sprintf(`{"name":"custom","steps":[{"name":"first","events":[%s,%s]}]}`, event, event),
		"trailing document":  input + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Custom(strings.NewReader(invalid), "custom-source"); err == nil {
				t.Error("Invalid custom scenario was accepted")
			}
		})
	}
}

// simulatorPlan supplies the exact assignment plan to sender and recovery
// tests without introducing prices or any financial state in the producer.
func simulatorPlan(t *testing.T) Plan {
	t.Helper()
	plan, err := Assignment("simulator-unit-test", 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
