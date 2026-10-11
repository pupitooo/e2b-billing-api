import test from "node:test";
import assert from "node:assert/strict";
import { APIClient } from "../../demo/transport.mjs";

// APIClient.send checkpoints the command before network I/O and preserves unknown outcomes.
test("APIClient.send", async (t) => {
  // Mutation regressions verify inputs that previously survived deliberate defects.
  await t.test("boundary regressions", async (t) => {
    const cases = [
      {
        "scenario": "failed checkpoint retains prior journal",
        "request": {
          "method": "POST",
          "path": "/customers/acme/credits",
          "body": "{}"
        },
        "storageFailure": true,
        "prior": [
          {
            "id": "prior",
            "body": "original"
          }
        ],
        "wantError": true,
        "wantCalls": 0,
        "wantHistory": [
          {
            "id": "prior",
            "body": "original"
          }
        ]
      },
      {
        "scenario": "protocol relative path",
        "request": {
          "method": "GET",
          "path": "//external.example/path"
        },
        "storageFailure": false,
        "prior": [],
        "wantError": true,
        "wantCalls": 0,
        "wantHistory": []
      },
      {
        "scenario": "503 response retains retry pacing",
        "request": {
          "method": "GET",
          "path": "/healthz"
        },
        "storageFailure": false,
        "prior": [],
        "wantError": false,
        "wantCalls": 1,
        "wantStatus": 503,
        "wantRetryAfter": "7"
      }
    ];
    for (const c of cases) {
      await t.test(c.scenario, async () => {
        let calls = 0, actual, error;
        const client = new APIClient({
          history: structuredClone(c.prior),
          save: () => {
            if (c.storageFailure) throw new Error("storage full");
          },
          fetcher: async () => {
            calls++;
            return new Response("{}", { status: 503, headers: { "Retry-After": "7" } });
          },
        });

        try {
          actual = await client.send(c.request);
        } catch (caught) {
          error = caught;
        }

        assert.equal(Boolean(error), c.wantError, `send request=${JSON.stringify(c.request)}; error=${error}; wantError=${c.wantError}`);
        assert.equal(calls, c.wantCalls, `send request=${JSON.stringify(c.request)}; calls=${calls}; want calls=${c.wantCalls}`);
        if (c.wantError) {
          assert.deepEqual(client.history, c.wantHistory, `send request=${JSON.stringify(c.request)}; want journal=${JSON.stringify(c.wantHistory)}`);
          return;
        }

        assert.equal(actual.status, c.wantStatus);
        assert.equal(actual.record.retryAfter, c.wantRetryAfter);
      });
    }
  });

  // The default browser fetch is bound to its native receiver, not the APIClient instance.
  await t.test("default fetch keeps the global browser receiver", async () => {
    const previousFetch = globalThis.fetch;
    let actualReceiver;
    globalThis.fetch = async function () {
      actualReceiver = this;
      return new Response("{}", { status: 200 });
    };
    try {
      const client = new APIClient({ save: () => {} });
      const actual = await client.send({ method: "GET", path: "/healthz" });
      assert.equal(actual.status, 200);
      assert.equal(actualReceiver, globalThis);
    } finally {
      globalThis.fetch = previousFetch;
    }
  });
  const cases = [
    {
      scenario: "committed success",
      inputRequest: {
        method: "POST",
        path: "/customers/acme/credits",
        body: '{"idempotency_key":"welcome","amount_cents":2500,"recorded_at":"2026-10-01T00:00:00Z"}',
      },
      inputNetworkFailure: false,
      inputStorageFailure: false,
      inputResponseStatus: 200,
      inputResponseBody: '{"amount_cents":2500}',
      wantStatus: 200,
      wantCalls: 1,
      wantError: false,
    },
    {
      scenario: "unknown commit after disconnect",
      inputRequest: {
        method: "POST",
        path: "/customers/acme/credits",
        body: '{"idempotency_key":"welcome","amount_cents":2500,"recorded_at":"2026-10-01T00:00:00Z"}',
      },
      inputNetworkFailure: true,
      inputStorageFailure: false,
      inputResponseStatus: 200,
      inputResponseBody: "{}",
      wantStatus: 0,
      wantCalls: 1,
      wantError: false,
    },
    {
      scenario: "no transmission without original checkpoint",
      inputRequest: {
        method: "POST",
        path: "/customers/acme/credits",
        body: '{"idempotency_key":"welcome","amount_cents":2500,"recorded_at":"2026-10-01T00:00:00Z"}',
      },
      inputNetworkFailure: false,
      inputStorageFailure: true,
      inputResponseStatus: 200,
      inputResponseBody: "{}",
      wantStatus: undefined,
      wantCalls: 0,
      wantError: true,
    },
  ];
  for (const c of cases)
    await t.test(c.scenario, async () => {
      let checkpoint = [],
        calls = [],
        actual,
        error;
      const client = new APIClient({
        save: (history) => {
          if (c.inputStorageFailure) throw new Error("storage full");
          checkpoint = JSON.parse(JSON.stringify(history));
        },
        fetcher: async (url, options) => {
          assert.equal(
            checkpoint[0].body,
            c.inputRequest.body,
            "original body must be persisted before fetch",
          );
          assert.equal(
            checkpoint[0].status,
            "sending",
            "checkpoint must precede outcome",
          );
          calls.push({ url, body: options.body, method: options.method });
          if (c.inputNetworkFailure) throw new Error("connection lost");
          return new Response(c.inputResponseBody, {
            status: c.inputResponseStatus,
          });
        },
      });
      try {
        actual = await client.send(c.inputRequest);
      } catch (caught) {
        error = caught;
      }
      assert.equal(
        Boolean(error),
        c.wantError,
        `send input=${JSON.stringify(c.inputRequest)}: error=${error}`,
      );
      assert.equal(calls.length, c.wantCalls);
      if (c.wantError) return;
      assert.equal(actual.status, c.wantStatus);
      assert.equal(client.history[0].body, c.inputRequest.body);
      assert.deepEqual(calls[0], {
        url: "/api/customers/acme/credits",
        body: c.inputRequest.body,
        method: "POST",
      });
    });
});

// APIClient.retry reuses the stored bytes after an unknown outcome, including unsafe-size integers.
test("APIClient.retry", async (t) => {
  const cases = [
    {
      scenario: "retry retains int64 and original timestamp",
      inputRequest: {
        method: "POST",
        path: "/usage/batches",
        body: '{"events":[{"units":9223372036854775807,"event_id":"one","period_start":"2026-10-10T12:00:00.000001Z"}]}',
      },
      inputResponses: [
        { failure: true },
        { failure: false, status: 202, body: '{"status":"accepted"}' },
      ],
      wantCalls: 2,
      wantStatus: 202,
    },
  ];
  for (const c of cases)
    await t.test(c.scenario, async () => {
      const sent = [];
      const client = new APIClient({
        save: () => {},
        fetcher: async (url, options) => {
          const response = c.inputResponses[sent.length];
          sent.push({ url, body: options.body });
          if (response.failure) throw new Error("reply lost");
          return new Response(response.body, { status: response.status });
        },
      });
      const first = await client.send(c.inputRequest);
      const second = await client.retry(first.record);
      assert.equal(second.status, c.wantStatus);
      assert.equal(sent.length, c.wantCalls);
      for (const call of sent) assert.equal(call.body, c.inputRequest.body);
      assert.equal(second.record.retryOf, first.record.id);
    });
});
