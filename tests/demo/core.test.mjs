import test from "node:test";
import assert from "node:assert/strict";
import {
  buildCommand,
  calendarDate,
  expectationDifferences,
  formatCents,
  formatMicrocents,
  parseExactJSON,
  invoiceEligible,
  timestampMicroseconds,
  usdToCents,
} from "../../demo/core.mjs";

// usdToCents rejects fractional cents and overflow while retaining the last int64 amount.
test("usdToCents", async (t) => {
  const cases = [
    {
      scenario: "assignment grant",
      input: "25.00",
      wantCents: 2500n,
      wantError: false,
    },
    { scenario: "one cent", input: "0.01", wantCents: 1n, wantError: false },
    {
      scenario: "exact int64 maximum",
      input: "92233720368547758.07",
      wantCents: 9223372036854775807n,
      wantError: false,
    },
    {
      scenario: "one cent beyond int64",
      input: "92233720368547758.08",
      wantCents: undefined,
      wantError: true,
    },
    {
      scenario: "fractional cent",
      input: "0.001",
      wantCents: undefined,
      wantError: true,
    },
    {
      scenario: "negative grant",
      input: "-1.00",
      wantCents: undefined,
      wantError: true,
    },
  ];
  for (const c of cases)
    await t.test(c.scenario, () => {
      let actual, error;
      try {
        actual = usdToCents(c.input);
      } catch (caught) {
        error = caught;
      }
      assert.equal(
        Boolean(error),
        c.wantError,
        `usdToCents input=${c.input}: error=${error}, wantError=${c.wantError}`,
      );
      if (!c.wantError)
        assert.equal(
          actual,
          c.wantCents,
          `usdToCents input=${c.input}: cents=${actual}, want=${c.wantCents}`,
        );
    });
});

// formatMicrocents covers the adjacent half-cent boundary and exact fractional-credit display.
test("formatMicrocents", async (t) => {
  const cases = [
    {
      scenario: "below half cent",
      input: "499999",
      exact: false,
      want: "$0.00",
    },
    {
      scenario: "exactly half cent",
      input: "500000",
      exact: false,
      want: "$0.01",
    },
    {
      scenario: "assignment default charge",
      input: "617283945",
      exact: false,
      want: "$6.17",
    },
    {
      scenario: "audit presentation",
      input: "617283945",
      exact: true,
      want: "$6.17283945",
    },
    {
      scenario: "fractional remaining credit",
      input: "990000",
      exact: true,
      want: "$0.00990000",
    },
    {
      scenario: "large exact money",
      input: "9223372036854775807000000",
      exact: true,
      want: "$92_233_720_368_547_758.07000000",
    },
  ];
  for (const c of cases)
    await t.test(c.scenario, () => {
      assert.equal(
        formatMicrocents(c.input, c.exact),
        c.want,
        `formatMicrocents input=${c.input} exact=${c.exact}`,
      );
    });
});

// formatCents displays signed invoice lines without floating-point conversion.
test("formatCents", async (t) => {
  const cases = [
    { scenario: "credit deduction", input: -1200, want: "−$12.00" },
    {
      scenario: "large invoice value",
      input: "9007199254740993",
      want: "$90_071_992_547_409.93",
    },
  ];
  for (const c of cases)
    await t.test(c.scenario, () => assert.equal(formatCents(c.input), c.want));
});

// parseExactJSON protects large integer fields, leaves strings alone, and rejects malformed input.
test("parseExactJSON", async (t) => {
  // Mutation regressions verify inputs that previously survived deliberate defects.
  await t.test("boundary regressions", async (t) => {
    const cases = [
      {
        "scenario": "last safe positive integer",
        "input": "{\"units\":9007199254740991}",
        "want": {
          "units": 9_007_199_254_740_991
        }
      },
      {
        "scenario": "first unsafe positive integer",
        "input": "{\"units\":9007199254740992}",
        "want": {
          "units": "9007199254740992"
        }
      }
    ];
    for (const c of cases) {
      await t.test(c.scenario, () => {
        assert.deepEqual(parseExactJSON(c.input), c.want, `parseExactJSON input=${c.input}; want=${JSON.stringify(c.want)}`);
      });
    }
  });

  const cases = [
    {
      scenario: "large financial field",
      input:
        '{"amount_cents":9223372036854775807,"name":"1234567890123456789"}',
      want: {
        amount_cents: "9223372036854775807",
        name: "1234567890123456789",
      },
      wantError: false,
    },
    {
      scenario: "negative large invoice field",
      input: '{"amount_cents":-9223372036854775807}',
      want: { amount_cents: "-9223372036854775807" },
      wantError: false,
    },
    {
      scenario: "safe integer stays numeric",
      input: '{"pending_events":2}',
      want: { pending_events: 2 },
      wantError: false,
    },
    {
      scenario: "invalid leading zero stays invalid",
      input: '{"units":09223372036854775807}',
      want: undefined,
      wantError: true,
    },
  ];
  for (const c of cases)
    await t.test(c.scenario, () => {
      let actual, error;
      try {
        actual = parseExactJSON(c.input);
      } catch (caught) {
        error = caught;
      }
      assert.equal(
        Boolean(error),
        c.wantError,
        `parseExactJSON input=${c.input}: error=${error}`,
      );
      if (!c.wantError) assert.deepEqual(actual, c.want);
    });
});

// buildCommand exposes every request body required by the current public API.
test("buildCommand", async (t) => {
  // Mutation regressions verify inputs that previously survived deliberate defects.
  await t.test("boundary regressions", async (t) => {
    const cases = [
      {
        "scenario": "zero grant",
        "operation": "credits",
        "input": {
          "idempotency_key": "zero",
          "amount": "0.00"
        },
        "wantError": true,
        "wantMessage": "outside the supported int64 range"
      },
      {
        "scenario": "equal interval endpoints",
        "operation": "usage",
        "input": {
          "source": "demo-ui/test",
          "event_id": "one",
          "sandbox_id": "sandbox",
          "metric": "cpu_seconds",
          "period_start": "2026-10-10T12:00:00.000001Z",
          "period_end": "2026-10-10T12:00:00.000001Z",
          "units": "1"
        },
        "wantError": true,
        "wantMessage": "end must be after its start"
      },
      {
        "scenario": "adjacent valid microseconds",
        "operation": "usage",
        "input": {
          "source": "demo-ui/test",
          "event_id": "one",
          "sandbox_id": "sandbox",
          "metric": "cpu_seconds",
          "period_start": "2026-10-10T12:00:00.000001Z",
          "period_end": "2026-10-10T12:00:00.000002Z",
          "units": "1"
        },
        "wantError": false,
        "wantBody": {
          "events": [
            {
              "schema_version": 1,
              "source": "demo-ui/test",
              "event_id": "one",
              "customer_id": "acme",
              "sandbox_id": "sandbox",
              "metric": "cpu_seconds",
              "period_start": "2026-10-10T12:00:00.000001Z",
              "period_end": "2026-10-10T12:00:00.000002Z",
              "units": 1
            }
          ]
        }
      }
    ];
    for (const c of cases) {
      await t.test(c.scenario, () => {
        let actual, error;
        try {
          actual = buildCommand(c.operation, c.input, "acme");
        } catch (caught) {
          error = caught;
        }

        assert.equal(Boolean(error), c.wantError, `buildCommand input=${JSON.stringify(c.input)}; error=${error}; wantError=${c.wantError}`);
        if (c.wantError) {
          assert.ok(error.message.includes(c.wantMessage), `error=${error.message}; want containing ${c.wantMessage}`);
          return;
        }

        assert.deepEqual(JSON.parse(actual.body), c.wantBody, `buildCommand input=${JSON.stringify(c.input)}; wantBody=${JSON.stringify(c.wantBody)}`);
      });
    }
  });

  const cases = [
    {
      scenario: "default price with a calendar date",
      operation: "prices",
      customer: "acme",
      input: {
        idempotency_key: "price-a",
        scope: "default",
        metric: "cpu_seconds",
        price: "0.06",
        effective_from: "2026-11-01",
      },
      wantPath: "/prices",
      wantBody: {
        idempotency_key: "price-a",
        customer_id: null,
        metric: "cpu_seconds",
        price_per_million_cents: 6,
        effective_from: "2026-11-01",
      },
      wantError: false,
    },
    {
      scenario: "future customer override",
      operation: "prices",
      customer: "acme",
      input: {
        idempotency_key: "price-b",
        scope: "customer",
        metric: "cpu_seconds",
        price: "0.04",
        effective_from: "2027-01-01",
      },
      wantPath: "/prices",
      wantBody: {
        idempotency_key: "price-b",
        customer_id: "acme",
        metric: "cpu_seconds",
        price_per_million_cents: 4,
        effective_from: "2027-01-01",
      },
      wantError: false,
    },
    {
      scenario: "credit grant",
      operation: "credits",
      customer: "acme",
      input: {
        idempotency_key: "welcome",
        amount: "25.00",
      },
      wantPath: "/customers/acme/credits",
      wantBody: {
        idempotency_key: "welcome",
        amount_cents: 2500,
      },
      wantError: false,
    },
    {
      scenario: "addon purchase",
      operation: "addons",
      customer: "acme",
      input: {
        idempotency_key: "pack",
        addon_name: "concurrency_pack",
        purchased_at: "2026-10-05",
      },
      wantPath: "/customers/acme/addons",
      wantBody: {
        idempotency_key: "pack",
        addon_name: "concurrency_pack",
        purchased_at: "2026-10-05",
      },
      wantError: false,
    },
    {
      scenario: "zero limit is not unlimited",
      operation: "spend-limit",
      customer: "cyberdyne",
      input: { idempotency_key: "zero", limit: "0.00" },
      wantPath: "/customers/cyberdyne/spend-limit",
      wantBody: { idempotency_key: "zero", limit_cents: 0 },
      wantError: false,
    },
    {
      scenario: "explicit unlimited limit",
      operation: "spend-limit",
      customer: "cyberdyne",
      input: { idempotency_key: "unlimited", unlimited: "on" },
      wantPath: "/customers/cyberdyne/spend-limit",
      wantBody: { idempotency_key: "unlimited", limit_cents: null },
      wantError: false,
    },
    {
      scenario: "invoice retry identity",
      operation: "invoices",
      customer: "acme",
      input: { month: "2026-10" },
      wantPath: "/customers/acme/invoices",
      wantBody: { month: "2026-10" },
      wantError: false,
    },
    {
      scenario: "missing operation key",
      operation: "credits",
      customer: "acme",
      input: { amount: "25.00" },
      wantPath: undefined,
      wantBody: undefined,
      wantError: true,
    },
  ];
  for (const c of cases)
    await t.test(c.scenario, () => {
      let actual, error;
      try {
        actual = buildCommand(c.operation, c.input, c.customer);
      } catch (caught) {
        error = caught;
      }
      assert.equal(
        Boolean(error),
        c.wantError,
        `buildCommand operation=${c.operation}: error=${error}`,
      );
      if (c.wantError) return;
      assert.equal(actual.method, "POST");
      assert.equal(actual.path, c.wantPath);
      assert.deepEqual(JSON.parse(actual.body), c.wantBody);
    });
  await t.test("maximum units remain a numeric JSON token", () => {
    const input = {
      source: "demo-ui/test",
      event_id: "one",
      sandbox_id: "sandbox",
      metric: "cpu_seconds",
      period_start: "2026-10-10T12:00:00.000001Z",
      period_end: "2026-10-10T12:00:00.000002Z",
      units: "9223372036854775807",
    };
    const wantToken = '"units": 9223372036854775807';
    const actual = buildCommand("usage", input, "acme");
    assert.ok(
      actual.body.includes(wantToken),
      `buildCommand input units=${input.units}: body=${actual.body}, want token=${wantToken}`,
    );
  });
});

// timestampMicroseconds distinguishes adjacent valid microseconds and equivalent offsets.
test("timestampMicroseconds", async (t) => {
  const cases = [
    {
      scenario: "adjacent microseconds",
      start: "2026-10-10T12:00:00.000001Z",
      end: "2026-10-10T12:00:00.000002Z",
      wantDifference: 1n,
    },
    {
      scenario: "same instant in Shanghai",
      start: "2026-10-10T12:00:00.123456Z",
      end: "2026-10-10T20:00:00.123456+08:00",
      wantDifference: 0n,
    },
  ];
  for (const c of cases)
    await t.test(c.scenario, () =>
      assert.equal(
        timestampMicroseconds(c.end) - timestampMicroseconds(c.start),
        c.wantDifference,
      ),
    );
});

// invoiceEligible keeps unfinished UTC months separate from the exact valid closing instant.
test("invoiceEligible", async (t) => {
  const cases = [
    {
      scenario: "last millisecond before boundary",
      month: "2026-10",
      now: "2026-10-31T23:59:59.999Z",
      wantEligible: false,
    },
    {
      scenario: "exact month-end instant",
      month: "2026-10",
      now: "2026-11-01T00:00:00Z",
      wantEligible: true,
    },
    {
      scenario: "future month",
      month: "2026-11",
      now: "2026-11-01T00:00:00Z",
      wantEligible: false,
    },
  ];
  for (const c of cases)
    await t.test(c.scenario, () =>
      assert.equal(invoiceEligible(c.month, new Date(c.now)), c.wantEligible),
    );
});

// expectationDifferences requires the complete invoice line count even when the expected row subset matches.
test("expectationDifferences", async (t) => {
  const cases = [
    {
      "scenario": "extra financial line",
      "actual": {
        "lines": [
          {
            "amount_cents": 2000
          },
          {
            "amount_cents": 1
          }
        ]
      },
      "expected": {
        "lines": [
          {
            "amount_cents": 2000
          }
        ]
      },
      "want": [
        "response.lines: expected 1 rows"
      ]
    },
    {
      "scenario": "matching financial lines",
      "actual": {
        "lines": [
          {
            "amount_cents": 2000
          }
        ]
      },
      "expected": {
        "lines": [
          {
            "amount_cents": 2000
          }
        ]
      },
      "want": []
    }
  ];
  for (const c of cases) {
    await t.test(c.scenario, () => {
      assert.deepEqual(expectationDifferences(c.actual, c.expected), c.want, `actual=${JSON.stringify(c.actual)}; expected=${JSON.stringify(c.expected)}; want differences=${JSON.stringify(c.want)}`);
    });
  }
});

// calendarDate preserves supported UTC dates and rejects timestamps and invalid Gregorian days.
test("calendarDate", async (t) => {
  const cases = [
    { scenario: "lower date", input: "1000-01-01", want: "1000-01-01", wantError: false },
    { scenario: "upper date", input: "9999-12-31", want: "9999-12-31", wantError: false },
    { scenario: "leap day", input: "2028-02-29", want: "2028-02-29", wantError: false },
    { scenario: "invalid leap day", input: "2026-02-29", want: undefined, wantError: true },
    { scenario: "timestamp", input: "2026-11-01T00:00:00Z", want: undefined, wantError: true },
    { scenario: "unsupported year", input: "0999-12-31", want: undefined, wantError: true },
    { scenario: "unpadded day", input: "2026-11-1", want: undefined, wantError: true },
  ];
  for (const c of cases)
    await t.test(c.scenario, () => {
      let actual, error;
      try { actual = calendarDate(c.input); } catch (caught) { error = caught; }

      assert.equal(Boolean(error), c.wantError, `calendarDate(${c.input}): error=${error}; wantError=${c.wantError}`);
      if (!c.wantError) assert.equal(actual, c.want);
    });
});
