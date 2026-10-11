import test from "node:test";
import assert from "node:assert/strict";

// invoice workflow executes the actual form and Load handlers in isolated browser
// contexts; changing only the form month must keep POST and GET on that month.
test("invoice workflow", async (t) => {
  const cases = [
    {
      scenario: "issue then load a month different from the header",
      inputCustomer: "cyberdyne",
      inputHeaderMonth: "2026-10",
      inputFormMonth: "2026-09",
      inputResponse: {
        customer_id: "cyberdyne", month: "2026-09", number: "CYBERDYNE-0001",
        lines: [], total_cents: 0, gross_usage_ticks: "0", credit_used_ticks: "0",
      },
      inputResponseStatus: 200,
      inputSteps: ["issue", "load"],
      wantRequests: [
        { method: "POST", path: "/api/customers/cyberdyne/invoices", body: { month: "2026-09" } },
        { method: "GET", path: "/api/customers/cyberdyne/invoices/2026-09", body: null },
      ],
      wantSnapshotNumber: "CYBERDYNE-0001",
      wantSnapshotMonth: "2026-09",
      wantStatus: 200,
      wantErrorMessage: null,
      wantToastMessage: "Operation completed. Response is visible below and in the inspector.",
    },
    {
      scenario: "load an existing invoice without issuing it again",
      inputCustomer: "acme",
      inputHeaderMonth: "2026-09",
      inputFormMonth: "2026-09",
      inputResponse: {
        customer_id: "acme", month: "2026-09", number: "ACME-0001",
        lines: [], total_cents: 0, gross_usage_ticks: "0", credit_used_ticks: "0",
      },
      inputResponseStatus: 200,
      inputSteps: ["load"],
      wantRequests: [
        { method: "GET", path: "/api/customers/acme/invoices/2026-09", body: null },
      ],
      wantSnapshotNumber: "ACME-0001",
      wantSnapshotMonth: "2026-09",
      wantStatus: 200,
      wantErrorMessage: null,
      wantToastMessage: "",
    },
    {
      scenario: "a missing invoice remains a visible 404 for the entered month",
      inputCustomer: "cyberdyne",
      inputHeaderMonth: "2026-09",
      inputFormMonth: "2026-10",
      inputResponse: { error: { code: "not_found", message: "The billing resource does not exist." } },
      inputResponseStatus: 404,
      inputSteps: ["load"],
      wantRequests: [
        { method: "GET", path: "/api/customers/cyberdyne/invoices/2026-10", body: null },
      ],
      wantSnapshotNumber: null,
      wantSnapshotMonth: null,
      wantStatus: 404,
      wantErrorMessage: "The billing resource does not exist.",
      wantToastMessage: "",
    },
    {
      scenario: "an empty form month is rejected before any request",
      inputCustomer: "cyberdyne",
      inputHeaderMonth: "2026-09",
      inputFormMonth: "",
      inputResponse: null,
      inputResponseStatus: null,
      inputSteps: ["load"],
      wantRequests: [],
      wantSnapshotNumber: null,
      wantSnapshotMonth: null,
      wantStatus: null,
      wantErrorMessage: "Use a UTC month as YYYY-MM, with a year from 1000 through 9999.",
      wantToastMessage: "Use a UTC month as YYYY-MM, with a year from 1000 through 9999.",
    },
  ];

  for (const c of cases) {
    await t.test(c.scenario, async (t) => {
      const requests = [];
      const browser = invoiceBrowser(t, c.inputCustomer, c.inputHeaderMonth);
      t.mock.method(globalThis, "fetch", async (path, options = {}) => {
        if (path === "/api/healthz") return new Response("{}", { status: 200 });
        if (path === "/simulator/billing-assignment.json")
          return new Response('{"steps":[]}', { status: 200 });

        requests.push({ method: options.method, path, body: options.body ? JSON.parse(options.body) : null });
        return new Response(JSON.stringify(c.inputResponse), { status: c.inputResponseStatus });
      });
      await import(`../../demo/app.mjs?scenario=${encodeURIComponent(c.scenario)}`);
      browser.month.value = c.inputFormMonth;
      let issuedHTML;

      for (const step of c.inputSteps) {
        if (step === "issue") {
          await browser.content.listeners.submit({ target: browser.form, preventDefault() {} });
          issuedHTML = browser.output.innerHTML;
        } else {
          await browser.content.listeners.click({ target: browser.load });
        }
      }

      const detail = `invoice workflow customer=${c.inputCustomer}, header=${c.inputHeaderMonth}, form=${c.inputFormMonth}`;
      assert.equal(browser.toast.textContent, c.wantToastMessage, `${detail}: message=${browser.toast.textContent}; want=${c.wantToastMessage}`);
      assert.deepEqual(requests, c.wantRequests, `${detail}: requests=${JSON.stringify(requests)}; want=${JSON.stringify(c.wantRequests)}`);
      assert.equal(browser.load.disabled, false, `${detail}: Load must remain enabled`);
      if (c.wantStatus === null) return;

      const journal = JSON.parse(browser.saved).history;
      assert.equal(journal[0].status, c.wantStatus, `${detail}: response status=${journal[0].status}; want=${c.wantStatus}`);
      if (c.wantErrorMessage) {
        assert.ok(browser.output.innerHTML.includes(c.wantErrorMessage), `${detail}: missing error=${c.wantErrorMessage}`);
        return;
      }

      assert.ok(browser.output.innerHTML.includes(c.wantSnapshotNumber), `${detail}: missing invoice number=${c.wantSnapshotNumber}`);
      assert.ok(browser.output.innerHTML.includes(`Month: ${c.wantSnapshotMonth}`), `${detail}: missing invoice month=${c.wantSnapshotMonth}`);
      if (issuedHTML !== undefined)
        assert.equal(browser.output.innerHTML, issuedHTML, `${detail}: Load must show the issued snapshot unchanged`);
    });
  }
});

// invoiceBrowser supplies only DOM and storage plumbing. Each scenario owns its
// fields, handlers and journal; network responses and expectations stay in cases.
function invoiceBrowser(t, customer, month) {
  const nodes = new Map();
  const node = (selector) => {
    if (!nodes.has(selector)) nodes.set(selector, {
      value: "", innerHTML: "", textContent: "", disabled: false,
      style: {}, listeners: {},
      addEventListener(event, handler) { this.listeners[event] = handler; },
    });
    return nodes.get(selector);
  };
  const browser = {
    content: node("#content"), output: node("#invoice-output"), toast: node("#toast"),
    month: node('#content form[data-operation="invoices"] [name="month"]'),
    saved: JSON.stringify({ customer, month, source: "demo-ui/invoice-test" }),
  };
  browser.form = {
    dataset: { operation: "invoices" },
    querySelector: () => node("[data-form-result]"),
    querySelectorAll: () => [],
  };
  browser.load = {
    id: "read-invoice", type: "button", disabled: false,
    closest() { return this; },
    hasAttribute() { return false; },
    dataset: {},
  };
  node("#customer").options = [{ value: customer }];
  const globals = {
    document: { querySelector: node },
    location: { hash: "#invoices" },
    window: { addEventListener() {} },
    localStorage: {
      getItem: () => browser.saved,
      setItem: (_key, value) => { browser.saved = value; },
    },
  };
  for (const [key, value] of Object.entries(globals)) {
    const prior = Object.getOwnPropertyDescriptor(globalThis, key);
    Object.defineProperty(globalThis, key, { value, writable: true, configurable: true });
    t.after(() => {
      if (prior) Object.defineProperty(globalThis, key, prior);
      else delete globalThis[key];
    });
  }
  t.mock.method(globalThis, "FormData", function () { return [["month", browser.month.value]]; });
  t.mock.method(globalThis, "setTimeout", () => 0);
  t.mock.method(globalThis, "setInterval", () => 0);
  return browser;
}
