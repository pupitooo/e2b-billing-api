import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

// invoice workflow executes the actual form and Load handlers in isolated browser
// contexts. POST and GET use the form month, and every Load visibly reports its
// progress and outcome, even when the immutable document remains unchanged.
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
      wantLoadingDisabled: true,
      wantLoadingButtonText: "Loading…",
      wantLoadingMessage: "Loading invoice for cyberdyne · 2026-09…",
      wantLoadMessage: "Invoice CYBERDYNE-0001 for 2026-09 loaded.",
      wantFinalDisabled: false,
      wantFinalButtonText: "Load invoice",
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
      wantLoadingDisabled: true,
      wantLoadingButtonText: "Loading…",
      wantLoadingMessage: "Loading invoice for acme · 2026-09…",
      wantLoadMessage: "Invoice ACME-0001 for 2026-09 loaded.",
      wantFinalDisabled: false,
      wantFinalButtonText: "Load invoice",
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
      wantLoadingDisabled: true,
      wantLoadingButtonText: "Loading…",
      wantLoadingMessage: "Loading invoice for cyberdyne · 2026-10…",
      wantLoadMessage: "The billing resource does not exist.",
      wantFinalDisabled: false,
      wantFinalButtonText: "Load invoice",
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
      wantLoadingDisabled: false,
      wantLoadingButtonText: "Load invoice",
      wantLoadingMessage: "",
      wantLoadMessage: "",
      wantFinalDisabled: false,
      wantFinalButtonText: "Load invoice",
    },
    {
      scenario: "network failure reports a visible error and allows another load",
      inputCustomer: "cyberdyne",
      inputHeaderMonth: "2026-10",
      inputFormMonth: "2026-09",
      inputNetworkError: "connection lost",
      inputResponse: null,
      inputResponseStatus: null,
      inputSteps: ["load"],
      wantRequests: [
        { method: "GET", path: "/api/customers/cyberdyne/invoices/2026-09", body: null },
      ],
      wantSnapshotNumber: null,
      wantSnapshotMonth: null,
      wantStatus: "unknown",
      wantErrorMessage: "connection lost",
      wantToastMessage: "",
      wantLoadingDisabled: true,
      wantLoadingButtonText: "Loading…",
      wantLoadingMessage: "Loading invoice for cyberdyne · 2026-09…",
      wantLoadMessage: "Invoice could not be loaded. See the response below.",
      wantFinalDisabled: false,
      wantFinalButtonText: "Load invoice",
    },
    {
      scenario: "storage failure restores the button and reports the unsent request",
      inputCustomer: "cyberdyne",
      inputHeaderMonth: "2026-10",
      inputFormMonth: "2026-09",
      inputStorageError: "storage full",
      inputResponse: null,
      inputResponseStatus: null,
      inputSteps: ["load"],
      wantRequests: [],
      wantSnapshotNumber: null,
      wantSnapshotMonth: null,
      wantStatus: null,
      wantErrorMessage: "Request was not sent: browser storage is unavailable (storage full). Export or clear the local journal and try again.",
      wantToastMessage: "Request was not sent: browser storage is unavailable (storage full). Export or clear the local journal and try again.",
      wantLoadingDisabled: true,
      wantLoadingButtonText: "Loading…",
      wantLoadingMessage: "Loading invoice for cyberdyne · 2026-09…",
      wantLoadMessage: "Request was not sent: browser storage is unavailable (storage full). Export or clear the local journal and try again.",
      wantFinalDisabled: false,
      wantFinalButtonText: "Load invoice",
    },
  ];

  for (const c of cases) {
    await t.test(c.scenario, async (t) => {
      const requests = [];
      const browser = invoiceBrowser(t, c.inputCustomer, c.inputHeaderMonth, c.inputStorageError);
      t.mock.method(globalThis, "fetch", async (path, options = {}) => {
        if (path === "/demo/runtime.json") return new Response('{"clock_mode":"real"}', { status: 200 });
        if (path === "/api/healthz") return new Response("{}", { status: 200 });
        if (path === "/simulator/billing-assignment.json")
          return new Response('{"steps":[]}', { status: 200 });

        requests.push({ method: options.method, path, body: options.body ? JSON.parse(options.body) : null });
        if (c.inputNetworkError) throw new Error(c.inputNetworkError);

        return new Response(JSON.stringify(c.inputResponse), { status: c.inputResponseStatus });
      });
      await import(`../../demo/app.mjs?scenario=${encodeURIComponent(c.scenario)}`);
      browser.month.value = c.inputFormMonth;
      let issuedHTML;
      const detail = `invoice workflow customer=${c.inputCustomer}, header=${c.inputHeaderMonth}, form=${c.inputFormMonth}`;

      for (const step of c.inputSteps) {
        if (step === "issue") {
          await browser.content.listeners.submit({ target: browser.form, preventDefault() {} });
          issuedHTML = browser.output.innerHTML;
        } else {
          const loading = browser.content.listeners.click({ target: browser.load });

          assert.equal(browser.load.disabled, c.wantLoadingDisabled, `${detail}: loading disabled=${browser.load.disabled}; want=${c.wantLoadingDisabled}`);
          assert.equal(browser.load.textContent, c.wantLoadingButtonText, `${detail}: loading button text=${browser.load.textContent}; want=${c.wantLoadingButtonText}`);
          assert.equal(browser.loadStatus.textContent, c.wantLoadingMessage, `${detail}: loading message=${browser.loadStatus.textContent}; want=${c.wantLoadingMessage}`);

          await loading;
        }
      }

      assert.equal(browser.toast.textContent, c.wantToastMessage, `${detail}: message=${browser.toast.textContent}; want=${c.wantToastMessage}`);
      assert.deepEqual(requests, c.wantRequests, `${detail}: requests=${JSON.stringify(requests)}; want=${JSON.stringify(c.wantRequests)}`);
      assert.equal(browser.load.disabled, c.wantFinalDisabled, `${detail}: Load must remain enabled`);
      assert.equal(browser.load.textContent, c.wantFinalButtonText, `${detail}: Load must restore its label`);
      assert.equal(browser.loadStatus.textContent, c.wantLoadMessage, `${detail}: load message=${browser.loadStatus.textContent}; want=${c.wantLoadMessage}`);
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

// walkthrough runtime loads the published portal clock, selects an alternate
// literal plan, and preserves separate cursors when switching back and forth.
test("walkthrough runtime", async (t) => {
  const cases = [
    { scenario: "fixed clock stays visible during scenario switches", inputRuntime: { clock_mode: "fixed", fixed_now: "2026-11-01T00:00:00Z" }, inputRuntimeStatus: 200, wantClockPrefix: "SIMULATED UTC 2026-11-01 00:00:00", wantOriginalCursor: 4, wantInvoiceCursor: 1 },
    { scenario: "real clock uses advisory browser time", inputRuntime: { clock_mode: "real" }, inputRuntimeStatus: 200, wantClockPrefix: "BROWSER UTC ", wantOriginalCursor: 4, wantInvoiceCursor: 1 },
    { scenario: "missing metadata is visibly unavailable", inputRuntime: {}, inputRuntimeStatus: 404, wantClockPrefix: "BROWSER UTC · metadata unavailable ", wantOriginalCursor: 4, wantInvoiceCursor: 1 },
  ];
  for (const c of cases) {
    await t.test(c.scenario, async (t) => {
      const browser = invoiceBrowser(t, "cyberdyne", "2026-10");
      browser.saved = JSON.stringify({ customer: "cyberdyne", month: "2026-10", source: "demo-ui/clock-test", assignment: { source: "demo-ui/clock-test", index: 4, captures: {}, completed: [], last: null } });
      globalThis.location.hash = "#assignment";
      const plans = {
        "billing-assignment": JSON.parse(await readFile(new URL("../../docs/simulator/billing-assignment.json", import.meta.url), "utf8")),
        "billing-demo-invoice": JSON.parse(await readFile(new URL("../../docs/simulator/billing-demo-invoice.json", import.meta.url), "utf8")),
      };
      const requests = [];
      t.mock.method(globalThis, "fetch", async (path, options = {}) => {
        if (path === "/api/healthz") return new Response("{}", { status: 200 });
        if (path === "/demo/runtime.json") return new Response(JSON.stringify(c.inputRuntime), { status: c.inputRuntimeStatus });
        if (path.startsWith("/simulator/")) return new Response(JSON.stringify(plans[path.split("/").pop().replace(".json", "")]), { status: 200 });
        requests.push({ path, method: options.method });
        return new Response('{"customer_id":"cyberdyne","credit_balance_ticks":"0","pending_events":0,"processing_errors":0,"state_version":0}', { status: 200 });
      });
      await import(`../../demo/app.mjs?clock=${encodeURIComponent(c.scenario)}`);
      await new Promise((resolve) => setImmediate(resolve));

      assert.ok(browser.clock.textContent.startsWith(c.wantClockPrefix), `clock=${browser.clock.textContent}; want prefix=${c.wantClockPrefix}`);
      await browser.content.listeners.change({ target: { id: "walkthrough-scenario", value: "billing-demo-invoice" } });
      assert.ok(browser.content.innerHTML.includes("October invoice walkthrough"), "selected plan must be displayed");
      await browser.content.listeners.click({ target: { id: "next-step", dataset: {}, closest() { return this; }, hasAttribute() { return false; } } });
      let saved = JSON.parse(browser.saved);
      assert.equal(saved.assignment.index, c.wantInvoiceCursor, "invoice walkthrough advances its own cursor");
      await browser.content.listeners.change({ target: { id: "walkthrough-scenario", value: "billing-assignment" } });
      saved = JSON.parse(browser.saved);
      assert.equal(saved.assignment.index, c.wantOriginalCursor, "assignment cursor must be retained");
      await browser.content.listeners.change({ target: { id: "walkthrough-scenario", value: "billing-demo-invoice" } });
      saved = JSON.parse(browser.saved);
      assert.equal(saved.assignment.index, c.wantInvoiceCursor, "invoice cursor must survive switching back");
      assert.deepEqual(requests, [{ path: "/api/customers/cyberdyne/credit", method: "GET" }], "switching plans performs no financial commands");
    });
  }
});

// invoiceBrowser supplies only DOM and storage plumbing. Each scenario owns its
// fields, handlers and journal; network responses and expectations stay in cases.
function invoiceBrowser(t, customer, month, storageError) {
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
    clock: node("#utc-clock"), content: node("#content"), output: node("#invoice-output"), toast: node("#toast"),
    loadStatus: node("#invoice-load-status"),
    month: node('#content form[data-operation="invoices"] [name="month"]'),
    saved: JSON.stringify({ customer, month, source: "demo-ui/invoice-test" }),
  };
  browser.form = {
    dataset: { operation: "invoices" },
    querySelector: () => node("[data-form-result]"),
    querySelectorAll: () => [],
  };
  browser.load = {
    id: "read-invoice", type: "button", disabled: false, textContent: "Load invoice",
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
      setItem: (_key, value) => {
        if (storageError) throw new Error(storageError);
        browser.saved = value;
      },
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
