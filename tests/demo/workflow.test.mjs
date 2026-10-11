import test from "node:test";
import assert from "node:assert/strict";
import { AssignmentRunner, workflowRequest, workflowDifferences } from "../../demo/workflow.mjs";

// workflowRequest replaces the source without interpreting user-provided quoting as JSON syntax.
test("workflowRequest", async (t) => {
  const cases = [
    {
      scenario: "quoted producer identity remains data",
      inputSource: 'demo-ui/a"b\\c',
      inputStep: {
        request: {
          method: "POST",
          path: "/usage/batches",
          body: {
            events: [{ source: "{{source}}", event_id: "one", units: 1 }],
          },
        },
      },
      wantRequest: { method: "POST", path: "/usage/batches" },
      wantBody: {
        events: [{ source: 'demo-ui/a"b\\c', event_id: "one", units: 1 }],
      },
    },
  ];
  for (const c of cases)
    await t.test(c.scenario, () => {
      const actual = workflowRequest(c.inputStep, c.inputSource);
      assert.equal(actual.method, c.wantRequest.method);
      assert.equal(actual.path, c.wantRequest.path);
      assert.deepEqual(JSON.parse(actual.body), c.wantBody);
    });
});

// AssignmentRunner.step advances only on literal expected results and retains retry identity after simulated loss.
test("AssignmentRunner.step", async (t) => {
  const cases = [
    {
      scenario: "lost reply retries exact credit command",
      inputStep: {
        name: "grant",
        request: {
          method: "POST",
          path: "/customers/acme/credits",
          body: {
            idempotency_key: "welcome",
            amount_cents: 2500,
          },
        },
        want: { status: 200, body: { amount_cents: 2500 } },
        fault: { lose_response: true },
        capture: "grant",
      },
      inputResponses: [
        { status: 200, data: { amount_cents: 2500 } },
        { status: 200, data: { amount_cents: 2500 } },
      ],
      wantCalls: 2,
      wantIndex: 1,
      wantDifferenceCount: 0,
      wantSavedCapture: { amount_cents: 2500 },
      wantBody:
        '{"idempotency_key":"welcome","amount_cents":2500}',
    },
    {
      scenario: "pending accounting is polled before advancing",
      inputStep: {
        name: "balance",
        request: { method: "GET", path: "/customers/acme/credit" },
        want: {
          status: 200,
          body: { credit_balance_microcents: "2100000000", pending_events: 0 },
        },
        await: true,
      },
      inputResponses: [
        {
          status: 200,
          data: { credit_balance_microcents: "2500000000", pending_events: 1 },
        },
        {
          status: 200,
          data: { credit_balance_microcents: "2100000000", pending_events: 0 },
        },
      ],
      wantCalls: 2,
      wantIndex: 1,
      wantDifferenceCount: 0,
      wantSavedCapture: undefined,
      wantBody: null,
    },
    {
      scenario: "unfinished month retains cursor",
      inputStep: {
        name: "invoice",
        request: {
          method: "POST",
          path: "/customers/acme/invoices",
          body: { month: "2026-10" },
        },
        want: { status: 200 },
      },
      inputResponses: [
        {
          status: 422,
          data: { error: { code: "invalid_request", field: "month" } },
        },
      ],
      wantCalls: 1,
      wantIndex: 0,
      wantDifferenceCount: 1,
      wantSavedCapture: undefined,
      wantBody: '{"month":"2026-10"}',
    },
    {
      scenario: "quarantine stops accounting polling",
      inputStep: {
        name: "balance",
        request: { method: "GET", path: "/customers/acme/credit" },
        want: { status: 200, body: { processing_errors: 0 } },
        await: true,
      },
      inputResponses: [{ status: 200, data: { processing_errors: 1 } }],
      wantCalls: 1,
      wantIndex: 0,
      wantDifferenceCount: 1,
      wantSavedCapture: undefined,
      wantBody: null,
    },
  ];
  for (const c of cases)
    await t.test(c.scenario, async () => {
      const calls = [],
        checkpoints = [];
      const state = {
        source: "demo-ui/test",
        index: 0,
        captures: {},
        completed: [],
      };
      const client = {
        send: async (request) => {
          assert.equal(
            checkpoints[0].index,
            0,
            "cursor must be saved before transmission",
          );
          const response = c.inputResponses[calls.length];
          calls.push(request);
          return { ...response, record: { ...request, id: "original" } };
        },
        retry: async (record) => {
          const response = c.inputResponses[calls.length];
          calls.push({
            method: record.method,
            path: record.path,
            body: record.body,
          });
          return { ...response, record: { id: "retry" } };
        },
      };
      const runner = new AssignmentRunner({
        plan: { steps: [c.inputStep] },
        state,
        client,
        save: (value) => checkpoints.push(JSON.parse(JSON.stringify(value))),
        sleep: async () => {},
      });
      const result = await runner.step();
      assert.equal(calls.length, c.wantCalls);
      assert.equal(state.index, c.wantIndex);
      assert.equal(result.differences.length, c.wantDifferenceCount);
      assert.deepEqual(state.captures.grant, c.wantSavedCapture);
      for (const call of calls) assert.equal(call.body, c.wantBody);
      assert.equal(runner.running, false);
    });
});

// workflowDifferences treats captured snapshots as complete immutable objects, detecting both removed and added fields.
test("workflowDifferences", async (t) => {
  const cases = [
    {
      "scenario": "removed captured field",
      "actual": {
        "total_cents": 2000
      },
      "captured": {
        "total_cents": 2000,
        "number": "ACME-0001"
      },
      "want": [
        "response.number: got undefined, expected \"ACME-0001\""
      ]
    },
    {
      "scenario": "added captured field",
      "actual": {
        "total_cents": 2000,
        "number": "ACME-0001"
      },
      "captured": {
        "total_cents": 2000
      },
      "want": [
        "response.number: got undefined, expected \"ACME-0001\""
      ]
    },
    {
      "scenario": "same snapshot",
      "actual": {
        "total_cents": 2000
      },
      "captured": {
        "total_cents": 2000
      },
      "want": []
    }
  ];
  for (const c of cases) {
    await t.test(c.scenario, () => {
      const step = { want: { status: 200, same_as: "invoice" } };
      const response = { status: 200, data: c.actual };
      const captures = { invoice: c.captured };

      assert.deepEqual(workflowDifferences(step, response, captures), c.want, `actual=${JSON.stringify(c.actual)}; captured=${JSON.stringify(c.captured)}; want differences=${JSON.stringify(c.want)}`);
    });
  }
});
