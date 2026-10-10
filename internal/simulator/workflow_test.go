package simulator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestMatchWorkflowResponse ensures billing assertions retain exact numbers,
// complete line arrays, and immutable captured responses while allowing extra
// generated object fields such as invoice identifiers and issue timestamps.
func TestMatchWorkflowResponse(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		want      WorkflowExpectation
		captures  map[string]json.RawMessage
		wantError bool
	}{
		{name: "captured price ID in invoice", status: 200, body: `{"lines":[{"kind":"usage","price_version_id":"generated-price","amount_cents":800}]}`, want: WorkflowExpectation{Status: 200, Body: json.RawMessage(`{"lines":[{"kind":"usage","price_version_id":"{{capture.created.price_version_id}}","amount_cents":800}]}`)}, captures: map[string]json.RawMessage{"created": json.RawMessage(`{"price_version_id":"generated-price"}`)}},
		{name: "missing price capture fails", status: 200, body: `{"price_version_id":"generated-price"}`, want: WorkflowExpectation{Status: 200, Body: json.RawMessage(`{"price_version_id":"{{capture.created.price_version_id}}"}`)}, wantError: true},
		{name: "missing captured field fails", status: 200, body: `{"price_version_id":"generated-price"}`, want: WorkflowExpectation{Status: 200, Body: json.RawMessage(`{"price_version_id":"{{capture.created.price_version_id}}"}`)}, captures: map[string]json.RawMessage{"created": json.RawMessage(`{"metric":"cpu_seconds"}`)}, wantError: true},
		{name: "captured generated IDs determine documented line order", status: 200, body: `{"lines":[{"kind":"usage","price_version_id":"a","amount_cents":300},{"kind":"usage","price_version_id":"z","amount_cents":800},{"kind":"credit","amount_cents":0}]}`, want: WorkflowExpectation{Status: 200, UsageLineOrder: "price_version_id", Body: json.RawMessage(`{"lines":[{"kind":"usage","price_version_id":"{{capture.default.price_version_id}}","amount_cents":800},{"kind":"usage","price_version_id":"{{capture.override.price_version_id}}","amount_cents":300},{"kind":"credit","amount_cents":0}]}`)}, captures: map[string]json.RawMessage{"default": json.RawMessage(`{"price_version_id":"z"}`), "override": json.RawMessage(`{"price_version_id":"a"}`)}},
		{name: "wrong generated-ID line order still fails", status: 200, body: `{"lines":[{"kind":"usage","price_version_id":"z","amount_cents":800},{"kind":"usage","price_version_id":"a","amount_cents":300}]}`, want: WorkflowExpectation{Status: 200, UsageLineOrder: "price_version_id", Body: json.RawMessage(`{"lines":[{"kind":"usage","price_version_id":"z","amount_cents":800},{"kind":"usage","price_version_id":"a","amount_cents":300}]}`)}, wantError: true},
		{name: "literal invoice subset", status: 200, body: `{"number":"ACME-0001","total_cents":2000,"lines":[{"kind":"credit","amount_cents":-1200}]}`, want: WorkflowExpectation{Status: 200, Body: json.RawMessage(`{"total_cents":2000,"lines":[{"kind":"credit","amount_cents":-1200}]}`)}},
		{name: "large integer difference remains visible", status: 200, body: `{"total_cents":9007199254740993}`, want: WorkflowExpectation{Status: 200, Body: json.RawMessage(`{"total_cents":9007199254740992}`)}, wantError: true},
		{name: "extra invoice line fails", status: 200, body: `{"lines":[{"amount_cents":2000},{"amount_cents":1}]}`, want: WorkflowExpectation{Status: 200, Body: json.RawMessage(`{"lines":[{"amount_cents":2000}]}`)}, wantError: true},
		{name: "line order is significant", status: 200, body: `{"lines":[{"kind":"credit"},{"kind":"addon"}]}`, want: WorkflowExpectation{Status: 200, Body: json.RawMessage(`{"lines":[{"kind":"addon"},{"kind":"credit"}]}`)}, wantError: true},
		{name: "captured snapshot ignores JSON key order", status: 200, body: `{"total_cents":2000,"number":"ACME-0001"}`, want: WorkflowExpectation{Status: 200, SameAs: "october"}, captures: map[string]json.RawMessage{"october": json.RawMessage(`{"number":"ACME-0001","total_cents":2000}`)}},
		{name: "any captured snapshot field change fails", status: 200, body: `{"number":"ACME-0002","total_cents":2000}`, want: WorkflowExpectation{Status: 200, SameAs: "october"}, captures: map[string]json.RawMessage{"october": json.RawMessage(`{"number":"ACME-0001","total_cents":2000}`)}, wantError: true},
		{name: "expected conflict checks public error", status: 409, body: `{"error":{"code":"billing_conflict","message":"already committed"}}`, want: WorkflowExpectation{Status: 409, Body: json.RawMessage(`{"error":{"code":"billing_conflict"}}`)}},
		{name: "successful body with wrong status fails", status: 503, body: `{"total_cents":2000}`, want: WorkflowExpectation{Status: 200, Body: json.RawMessage(`{"total_cents":2000}`)}, wantError: true},
		{name: "trailing JSON is rejected", status: 200, body: `{} {}`, want: WorkflowExpectation{Status: 200}, wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := matchWorkflowResponse(workflowResponse{status: tc.status, body: []byte(tc.body)}, tc.want, tc.captures)
			if (err != nil) != tc.wantError {
				t.Fatalf("matchWorkflowResponse(status=%d body=%s want=%+v): error=%v wantError=%t", tc.status, tc.body, tc.want, err, tc.wantError)
			}
		})
	}
}

// TestOpenWorkflowState preserves the original plan and producer namespace:
// a changed plan/source or legacy transport file cannot restart at a wrong step.
func TestOpenWorkflowState(t *testing.T) {
	cases := []struct {
		name            string
		originalSource  string
		restartSource   string
		restartPath     string
		initialContents string
		wantError       bool
	}{
		{name: "identical restart", originalSource: "platform", restartSource: "platform", restartPath: "/healthz"},
		{name: "producer identity changed", originalSource: "platform", restartSource: "another-platform", restartPath: "/healthz", wantError: true},
		{name: "public operation changed", originalSource: "platform", restartSource: "platform", restartPath: "/customers/acme/credit", wantError: true},
		{name: "legacy transport state retained", originalSource: "platform", restartSource: "platform", restartPath: "/healthz", initialContents: `{"version":1,"source":"platform","plan":{"name":"assignment"}}`, wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workflow.json")
			store, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			options := WorkflowOptions{Action: "run", Source: tc.originalSource, Plan: Workflow{Name: "billing-test", Steps: []WorkflowStep{{Name: "health", Request: WorkflowRequest{Method: "GET", Path: "/healthz"}, Want: WorkflowExpectation{Status: 200}}}}}
			if tc.initialContents != "" {
				if err := os.WriteFile(path, []byte(tc.initialContents), 0600); err != nil {
					t.Fatal(err)
				}
			} else if _, err := openWorkflowState(store, options); err != nil {
				t.Fatal(err)
			}

			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			options.Source = tc.restartSource
			options.Plan.Steps[0].Request.Path = tc.restartPath
			_, err = openWorkflowState(store, options)
			if (err != nil) != tc.wantError {
				t.Fatalf("openWorkflowState(source=%s path=%s): error=%v wantError=%t", tc.restartSource, tc.restartPath, err, tc.wantError)
			}

			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			if string(before) != string(after) {
				t.Fatal("opening a saved workflow changed its durable checkpoint")
			}
		})
	}
}
