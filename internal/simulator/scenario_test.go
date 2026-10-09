package simulator

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Assignment preserves the six binding consumption totals across sandbox and
// interval splits, generates deterministic identities, and rejects invalid bounds.
func TestAssignment(t *testing.T) {
	t.Run("simulator assignment totals and splitting", func(t *testing.T) {
		wantTotals := map[string]int64{
			"acme/2026-10-10": 100_000_000, "cyberdyne/2026-10-10": 123_456_789,
			"acme/2026-10-20": 200_000_000, "cyberdyne/2026-10-20": 200_000_000,
			"acme/2026-10-30": 50_000_000, "acme/2026-11-03": 100_000_000,
		}
		tests := []struct {
			name              string
			source            string
			sandboxes         int
			interval          time.Duration
			wantEventCount    int
			wantDeterministic bool
			wantBarrier       bool
		}{
			{
				name:              "hourly splits across three sandboxes",
				source:            "test-source",
				sandboxes:         3,
				interval:          time.Hour,
				wantEventCount:    18,
				wantDeterministic: true,
				wantBarrier:       true,
			},
			{
				name:              "minute splits across three sandboxes",
				source:            "test-source",
				sandboxes:         3,
				interval:          time.Minute,
				wantEventCount:    1_080,
				wantDeterministic: true,
				wantBarrier:       true,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				plan, err := Assignment(tt.source, tt.sandboxes, tt.interval)
				if err != nil {
					t.Fatal(err)
				}
				again, err := Assignment(tt.source, tt.sandboxes, tt.interval)
				if err != nil || sameJSON(plan, again) != tt.wantDeterministic {
					t.Fatalf("Plan is not deterministic: %v", err)
				}
				if count := len(plan.events()); count != tt.wantEventCount {
					t.Fatalf("Event count = %d", count)
				}
				totals := make(map[string]int64)
				previous := make(map[string]time.Time)
				for _, event := range plan.events() {
					key := event.CustomerID + "/" + event.PeriodStart.Format("2006-01-02")
					totals[key] += event.Units
					if event.Units < 0 || event.PeriodStart.Location() != time.UTC || event.PeriodEnd.Sub(event.PeriodStart) != tt.interval {
						t.Errorf("Invalid measurement split: %+v", event)
					}
					sandbox := key + "/" + event.SandboxID
					if end, present := previous[sandbox]; present && !end.Equal(event.PeriodStart) {
						t.Errorf("Intervals do not meet for %s", sandbox)
					}
					previous[sandbox] = event.PeriodEnd
				}
				for key, units := range wantTotals {
					if totals[key] != units {
						t.Errorf("%s = %d, want %d", key, totals[key], units)
					}
				}
				if len(totals) != len(wantTotals) || (plan.Steps[2].Barrier != "") != tt.wantBarrier {
					t.Error("Unexpected fixture groups or missing late-October barrier")
				}
			})
		}
	})
	t.Run("simulator assignment bounds", func(t *testing.T) {
		tests := []struct {
			name      string
			source    string
			sandboxes int
			interval  time.Duration
			wantError bool
		}{
			{
				name:      "no sandboxes",
				source:    "test",
				sandboxes: 0,
				interval:  time.Hour,
				wantError: true,
			},
			{
				name:      "too many sandboxes",
				source:    "test",
				sandboxes: 1_001,
				interval:  time.Hour,
				wantError: true,
			},
			{
				name:      "interval below a minute",
				source:    "test",
				sandboxes: 1,
				interval:  59 * time.Second,
				wantError: true,
			},
			{
				name:      "interval does not divide an hour",
				source:    "test",
				sandboxes: 1,
				interval:  7 * time.Minute,
				wantError: true,
			},
			{
				name:      "plan exceeds event budget",
				source:    "test",
				sandboxes: 1_000,
				interval:  time.Minute,
				wantError: true,
			},
			{
				name:      "non-minute exact divisor",
				source:    "test",
				sandboxes: 2,
				interval:  90 * time.Second,
				wantError: false,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := Assignment(tt.source, tt.sandboxes, tt.interval)
				if (err != nil) != tt.wantError {
					t.Errorf("Assignment(%q, %d, %s) error = %v; want error=%t", tt.source, tt.sandboxes, tt.interval, err, tt.wantError)
				}
			})
		}
	})
}

// Custom requires explicit measurement values, normalizes supplied instants
// to UTC, and rejects malformed or ambiguous scenario identities.
func TestCustom(t *testing.T) {
	t.Run("simulator custom explicit fields", func(t *testing.T) {
		const event = `{"event_id":"one","schema_version":1,"customer_id":"acme","sandbox_id":"one","metric":"cpu_seconds","period_start":"2026-10-10T20:00:00+08:00","period_end":"2026-10-10T20:01:00+08:00","units":0}`
		validInput := fmt.Sprintf(`{"name":"custom","steps":[{"name":"first","events":[%s]}]}`, event)
		tests := []struct {
			name            string
			input           string
			source          string
			wantError       bool
			wantSource      string
			wantUnits       int64
			wantPeriodStart string
		}{
			{
				name:            "explicit zero and offset instant",
				input:           validInput,
				source:          "custom-source",
				wantError:       false,
				wantSource:      "custom-source",
				wantUnits:       0,
				wantPeriodStart: "2026-10-10T12:00:00Z",
			},
			{
				name:      "missing units",
				input:     strings.Replace(validInput, `,"units":0`, "", 1),
				source:    "custom-source",
				wantError: true,
			},
			{
				name:      "null units",
				input:     strings.Replace(validInput, `"units":0`, `"units":null`, 1),
				source:    "custom-source",
				wantError: true,
			},
			{
				name:      "negative units",
				input:     strings.Replace(validInput, `"units":0`, `"units":-1`, 1),
				source:    "custom-source",
				wantError: true,
			},
			{
				name:      "unknown field",
				input:     strings.Replace(validInput, `"units":0`, `"units":0,"cost":10`, 1),
				source:    "custom-source",
				wantError: true,
			},
			{
				name:      "duplicate identity",
				input:     fmt.Sprintf(`{"name":"custom","steps":[{"name":"first","events":[%s,%s]}]}`, event, event),
				source:    "custom-source",
				wantError: true,
			},
			{
				name:      "trailing document",
				input:     validInput + `{}`,
				source:    "custom-source",
				wantError: true,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				plan, err := Custom(strings.NewReader(tt.input), tt.source)
				if (err != nil) != tt.wantError {
					t.Fatalf("Custom(%s) error = %v; want error=%t", tt.input, err, tt.wantError)
				}
				if tt.wantError {
					return
				}
				got := plan.events()[0]
				if got.Source != tt.wantSource || got.Units != tt.wantUnits || got.PeriodStart.Format(time.RFC3339) != tt.wantPeriodStart {
					t.Errorf("Custom event = %+v; want source %q, units %d, start %s", got, tt.wantSource, tt.wantUnits, tt.wantPeriodStart)
				}
			})
		}
	})
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
