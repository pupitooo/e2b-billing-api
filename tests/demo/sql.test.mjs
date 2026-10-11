import test from "node:test";
import assert from "node:assert/strict";
import {
  adminerURL,
  inspectionSQL,
  maintenanceSQL,
  sqlLiteral,
} from "../../demo/sql.mjs";

// sqlLiteral retains quotes/backslashes as one PostgreSQL literal and rejects NUL.
test("sqlLiteral", async (t) => {
  const cases = [
    {
      scenario: "quoted injection attempt remains a literal",
      input: "acme'; DROP TABLE customers; --",
      want: "E'acme''; DROP TABLE customers; --'",
      wantError: false,
    },
    {
      scenario: "backslash is explicitly escaped",
      input: "back\\slash",
      want: "E'back\\\\slash'",
      wantError: false,
    },
    {
      scenario: "NUL is unsupported",
      input: "acme\0",
      want: undefined,
      wantError: true,
    },
  ];
  for (const c of cases)
    await t.test(c.scenario, () => {
      let actual, error;
      try {
        actual = sqlLiteral(c.input);
      } catch (caught) {
        error = caught;
      }
      assert.equal(Boolean(error), c.wantError);
      if (!c.wantError) assert.equal(actual, c.want);
    });
});

// adminerURL prepopulates a stock PostgreSQL SQL editor without password or execute flags.
test("adminerURL", async (t) => {
  const cases = [
    {
      scenario: "query survives URL decoding",
      inputSQL: "SELECT * FROM usage_inbox WHERE customer_id = E'acme';",
      wantPath: "/database/",
      wantServer: "postgres",
      wantUsername: "e2b",
      wantDatabase: "e2b_billing",
      wantSQL: "SELECT * FROM usage_inbox WHERE customer_id = E'acme';",
    },
  ];
  for (const c of cases)
    await t.test(c.scenario, () => {
      const actual = new URL(adminerURL(c.inputSQL), "http://127.0.0.1:8082");
      assert.equal(actual.pathname, c.wantPath);
      assert.equal(actual.searchParams.get("pgsql"), c.wantServer);
      assert.equal(actual.searchParams.get("username"), c.wantUsername);
      assert.equal(actual.searchParams.get("db"), c.wantDatabase);
      assert.equal(actual.searchParams.get("sql"), c.wantSQL);
      assert.equal(actual.searchParams.has("password"), false);
      assert.equal(actual.searchParams.has("execute"), false);
    });
});

// inspectionSQL uses current command scopes and nested price payloads, plus UTC periods.
test("inspectionSQL", async (t) => {
  const cases = [
    {
      scenario: "account command history uses current scopes",
      inputView: "operations",
      inputCustomer: "acme",
      inputMonth: "2026-10",
      wantFragments: [
        "operation_scope = 'addons/' || E'acme'",
        "operation_scope = 'credits/' || E'acme'",
        "operation_scope = 'addon-cancellations/' || E'acme'",
        "operation_scope = 'invoice-catch-up/' || E'acme'",
        "operation_scope = 'spend-limit/' || E'acme'",
        "operation_scope = 'prices'",
        "request_payload->'price'->>'customer_id' = E'acme'",
      ],
      wantError: false,
    },
    {
      scenario: "credit inspection uses internal ledger IDs",
      inputView: "credit", inputCustomer: "acme", inputMonth: "2026-10",
      wantFragments: ["SELECT credit_entry_id, group_id, amount_microcents", "FROM credit_entries"],
      wantAbsentFragments: ["operation_id", "idempotency_key"], wantError: false,
    },
    {
      scenario: "limit history uses the shared command table",
      inputView: "limits", inputCustomer: "acme", inputMonth: "2026-10",
      wantFragments: ["FROM api_idempotency_operations", "operation_scope = 'spend-limit/' || E'acme'", "response_payload->'limit_cents'"],
      wantAbsentFragments: ["spend_limit_operations"], wantError: false,
    },
    {
      scenario: "UTC inbox month bounds",
      inputView: "inbox",
      inputCustomer: "cyberdyne",
      inputMonth: "2026-10",
      wantFragments: [
        "customer_id = E'cyberdyne'",
        "E'2026-10-01'::date",
        "AT TIME ZONE 'UTC'",
        "LIMIT 200",
      ],
      wantError: false,
    },
    {
      scenario: "invalid month cannot enter SQL",
      inputView: "inbox",
      inputCustomer: "acme",
      inputMonth: "2026-10'; DROP",
      wantFragments: [],
      wantError: true,
    },
  ];
  for (const c of cases)
    await t.test(c.scenario, () => {
      let actual, error;
      try {
        actual = inspectionSQL(c.inputView, {
          customer: c.inputCustomer,
          month: c.inputMonth,
        });
      } catch (caught) {
        error = caught;
      }
      assert.equal(Boolean(error), c.wantError);
      if (c.wantError) return;
      for (const fragment of c.wantFragments)
        assert.ok(
          actual.includes(fragment),
          `view=${c.inputView}: missing ${fragment}`,
        );
      for (const fragment of c.wantAbsentFragments ?? [])
        assert.equal(actual.includes(fragment), false, `view=${c.inputView}: retired field/table ${fragment}`);
    });
});

// maintenanceSQL limits deletion to unrated pending GUI receipts and releases only exact repaired identities.
test("maintenanceSQL", async (t) => {
  const cases = [
    {
      scenario: "pending deletion retains financial history",
      inputAction: "delete-pending",
      inputSource: "demo-ui/test",
      inputEvent: "one",
      wantFragments: [
        "source = E'demo-ui/test' AND event_id = E'one'",
        "processed_at IS NULL AND processing_error IS NULL",
        "NOT EXISTS (SELECT 1 FROM usage_ratings",
        "DELETE FROM usage_inbox",
        "COMMIT;",
      ],
      wantError: false,
    },
    {
      scenario: "exact error release",
      inputAction: "release-error",
      inputSource: "demo-ui/test",
      inputEvent: "one",
      wantFragments: [
        "source = E'demo-ui/test' AND event_id = E'one'",
        "processing_error IS NOT NULL",
        "SET processing_error = NULL",
        "NOT EXISTS (SELECT 1 FROM usage_ratings",
      ],
      wantError: false,
    },
    {
      scenario: "non-GUI sources are rejected",
      inputAction: "delete-pending",
      inputSource: "platform-simulator",
      inputEvent: "one",
      wantFragments: [],
      wantError: true,
    },
    {
      scenario: "release requires event identity",
      inputAction: "release-error",
      inputSource: "demo-ui/test",
      inputEvent: "",
      wantFragments: [],
      wantError: true,
    },
  ];
  for (const c of cases)
    await t.test(c.scenario, () => {
      let actual, error;
      try {
        actual = maintenanceSQL(c.inputAction, {
          source: c.inputSource,
          eventId: c.inputEvent,
        });
      } catch (caught) {
        error = caught;
      }
      assert.equal(Boolean(error), c.wantError);
      if (c.wantError) return;
      for (const fragment of c.wantFragments)
        assert.ok(actual.includes(fragment));
      assert.equal(
        /TRUNCATE|DISABLE TRIGGER|DELETE FROM (credit_entries|invoices|rated_usage_groups)/.test(
          actual,
        ),
        false,
      );
    });
});
