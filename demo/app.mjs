import { businessTime, loadRuntime } from "./runtime.mjs";
import { APIClient } from "./transport.mjs";
import { AssignmentRunner, workflowRequest } from "./workflow.mjs";
import {
  buildCommand,
  customerPath,
  formatCents,
  formatTicks,
  groupDigits,
  invoiceEligible,
  parseExactJSON,
  validMonth,
} from "./core.mjs";
import {
  adminerURL,
  inspectionSQL,
  inspectionViews,
  maintenanceSQL,
} from "./sql.mjs";

const STORAGE_KEY = "e2b.billing-demo.v1";
const DEFAULT_MONTH = "2026-10";
const TOAST_MS = 7_000;
const HEALTH_POLL_MS = 30_000;
const seedCustomers = [
  {
    id: "acme",
    name: "Acme Inc.",
    country: "US",
    address: "1 Market St, San Francisco, CA 94105",
  },
  {
    id: "cyberdyne",
    name: "Cyberdyne Systems Corporation",
    country: "US",
    address: "18144 El Camino Real, Sunnyvale, CA 94087",
  },
];
const sections = [
  { id: "overview", title: "Overview", icon: "◴", role: "ACCOUNT VISIBILITY" },
  { id: "customers", title: "Customers", icon: "♧", role: "SEEDED ACCOUNTS" },
  { id: "usage", title: "Usage", icon: "▥", role: "SANDBOX PLATFORM" },
  { id: "prices", title: "Prices", icon: "◇", role: "E2B CATALOG OPERATIONS" },
  { id: "credit", title: "Credit", icon: "⊕", role: "E2B CREDIT OPERATIONS" },
  { id: "addons", title: "Add-ons", icon: "▣", role: "CUSTOMER PURCHASES" },
  {
    id: "limits",
    title: "Limits",
    icon: "◷",
    role: "CUSTOMER BUDGET · PLATFORM STATUS",
  },
  { id: "invoices", title: "Invoices", icon: "▤", role: "MONTHLY BILLING" },
  {
    id: "assignment",
    title: "Assignment demo",
    icon: "▷",
    role: "PUBLIC API WALKTHROUGH",
  },
  {
    id: "database",
    title: "Database",
    icon: "▱",
    role: "MANUAL SQL INSPECTION",
  },
  {
    id: "service",
    title: "Service",
    icon: "⚙",
    role: "LOCAL DEMO MAINTENANCE",
  },
  {
    id: "history",
    title: "Request history",
    icon: "↺",
    role: "SAVED REQUESTS · EXACT RETRIES",
  },
];
const $ = (selector) => document.querySelector(selector);
const x = (value) =>
  String(value ?? "").replace(
    /[&<>"']/g,
    (character) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        character
      ],
  );
const pretty = (value) => JSON.stringify(value, null, 2);
const newKey = () => `demo-${crypto.randomUUID()}`;
let runtime = null;
const now = () => businessTime(runtime).toISOString();
let storageFailure = "";
let saved = {};
try {
  saved = JSON.parse(localStorage.getItem(STORAGE_KEY) || "{}");
} catch (error) {
  storageFailure = error.message;
}
let state = {
  customer: saved.customer || "acme",
  month: saved.month || DEFAULT_MONTH,
  source: saved.source || `demo-ui/${crypto.randomUUID()}`,
  history: saved.history || [],
  assignment: saved.assignment || null,
  walkthroughScenario: saved.walkthroughScenario || "billing-assignment",
  walkthroughs: saved.walkthroughs || {},
};
let caches = new Map();
let plan = null;
let runner = null;
let selectedStep = null;
let toastTimer;
let renderGeneration = 0;
let lastRecord = null;

function saveState() {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(state));
}
function flash(message, error = false) {
  const toast = $("#toast");
  toast.textContent = message;
  toast.hidden = false;
  toast.style.borderColor = error ? "var(--red)" : "var(--accent)";
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    toast.hidden = true;
  }, TOAST_MS);
}
function describeError(error) {
  flash(error.message, true);
}
function currentSection() {
  return location.hash.slice(1).split("?")[0] || "overview";
}
function requestDetails(record) {
  return `<div class="inspector-columns"><div><h4>${x(record.method)} /api${x(record.path)}</h4><pre>${x(record.body || "(no request body)")}</pre><p class="hint">${x(record.started)}${record.retryOf ? " · Exact retry of a saved request" : ""}</p></div><div><h4>RESPONSE · ${x(record.status)}</h4><pre>${x(record.response || "Waiting for response…")}</pre>${record.retryAfter ? `<p class="hint">Retry-After: ${x(record.retryAfter)} seconds. Retry manually with the original request.</p>` : ""}${record.storageWarning ? `<p class="notice error">${x(record.storageWarning)}</p>` : ""}</div></div>`;
}
const client = new APIClient({
  history: state.history,
  save: (history) => {
    state.history = history;
    saveState();
  },
  changed: (record) => {
    lastRecord = record;
    $("#last-request").textContent =
      `${record.method} ${record.path} · ${record.status}`;
    $("#request-detail").innerHTML = requestDetails(record);
    if ($("#recent-history"))
      $("#recent-history").innerHTML = historyTable(client.history.slice(0, 6));
    $("#footer-state").textContent =
      record.status === "sending"
        ? "Request in flight"
        : record.status === 202
          ? "Receipt accepted · accounting is asynchronous"
          : `Last response: ${record.status}`;
  },
});

function dbButton(view, label = "View in database", css = "small") {
  return `<a class="button ${css}" target="_blank" rel="noopener" href="${x(adminerURL(inspectionSQL(view, state)))}">↗ ${x(label)}</a>`;
}
function intro(title, description, actions = "") {
  return `<div class="page-intro"><div><h2>${x(title)}</h2><p>${x(description)}</p></div><div class="actions">${actions}</div></div>`;
}
function panel(title, body, actions = "") {
  return `<section class="panel"><div class="panel-heading"><h3>${x(title)}</h3><div class="actions">${actions}</div></div><div class="panel-body">${body}</div></section>`;
}
function notice(text, kind = "") {
  return `<p class="notice ${kind}">${x(text)}</p>`;
}
function empty(title, detail) {
  return `<div class="empty"><strong>${x(title)}</strong>${x(detail)}</div>`;
}
function field(
  name,
  title,
  value,
  { type = "text", full = false, hint = "", required = true } = {},
) {
  return `<label class="field ${full ? "full" : ""}">${x(title)}<input name="${x(name)}" type="${x(type)}" value="${x(value)}" ${required ? "required" : ""} ${type === "text" ? 'autocomplete="off"' : ""}>${hint ? `<small>${x(hint)}</small>` : ""}</label>`;
}
function select(name, title, options, hint = "") {
  return `<label class="field">${x(title)}<select name="${x(name)}">${options.map((option) => `<option value="${x(option.value)}">${x(option.label)}</option>`).join("")}</select>${hint ? `<small>${x(hint)}</small>` : ""}</label>`;
}
function operationForm(operation, title, fields, submit = "Send request") {
  return panel(
    title,
    `<form data-operation="${operation}"><div class="form-grid">${fields}</div><div class="actions"><button type="submit" class="primary">${x(submit)}</button><button type="button" data-preview>Preview request</button>${operation !== "usage" && operation !== "invoices" ? '<button type="button" data-new-key>New operation key</button>' : ""}</div><p class="hint">Submitting again keeps the same operation identity. Use the request inspector or history for an exact retry.</p><div data-form-result class="inline-result"></div></form>`,
  );
}
function statusBadge(status) {
  const color =
    status === 202
      ? "yellow"
      : Number(status) >= 200 && Number(status) < 300
        ? "green"
        : status === "sending"
          ? "muted"
          : "red";
  return `<span class="badge ${color}">${x(status === 202 ? "202 · ACCEPTED" : status)}</span>`;
}
function resultHTML(result) {
  const message =
    result.status === 202
      ? "Stored in the inbox. Refresh account status to observe worker completion."
      : result.status >= 200 && result.status < 300
        ? result.record.method === "GET"
          ? "Current API snapshot loaded."
          : "Operation completed. Identical retries retain the original result."
        : result.data?.error?.message || result.text || "Request failed.";
  return `<div class="result-heading">${statusBadge(result.status)}<strong>${x(message)}</strong></div><pre>${x(result.data ? pretty(result.data) : result.text)}</pre>`;
}
async function read(path, label) {
  const result = await client.send({ method: "GET", path }, label);
  if (result.status !== 200)
    throw new Error(
      result.data?.error?.message ||
        `${label}: HTTP ${result.status}. ${result.text}`,
    );
  return result.data;
}
async function refreshCustomer(customer = state.customer, month = state.month) {
  const [credit, limit] = await Promise.all([
    read(customerPath(customer, "credit"), `${customer} credit balance`),
    read(
      customerPath(customer, `months/${month}/limit-status`),
      `${customer} ${month} limit status`,
    ),
  ]);
  caches.set(`${customer}/${month}`, { credit, limit, fetched: new Date().toISOString() });
  return { credit, limit, fetched: new Date().toISOString() };
}
function metricsHTML(snapshot) {
  const credit = snapshot?.credit;
  const limit = snapshot?.limit;
  const cards = [
    {
      label: "Remaining credit",
      value: credit ? formatTicks(credit.credit_balance_ticks) : "—",
      note: credit
        ? `${formatTicks(credit.credit_balance_ticks, true)} exact`
        : "Refresh to read API",
    },
    {
      label: "Gross usage · " + state.month,
      value: limit ? formatTicks(limit.gross_charge_ticks) : "—",
      note: limit
        ? `${formatTicks(limit.gross_charge_ticks, true)} exact\nBefore credit · add-ons excluded`
        : "Before credit · add-ons excluded",
    },
    {
      label: "Monthly limit",
      value: limit
        ? limit.limit_cents === null
          ? "Unlimited"
          : formatCents(limit.limit_cents)
        : "—",
      note: limit
        ? limit.limit_reached
          ? "REACHED · billing continues"
          : "Below limit"
        : "Refresh to read API",
      color: limit?.limit_reached ? "bad" : "good",
    },
    {
      label: "Accounting",
      value: credit ? `${groupDigits(credit.pending_events)} pending` : "—",
      note: credit
        ? `${credit.processing_errors} errors · version ${groupDigits(credit.state_version)}`
        : "Acceptance and accounting are separate",
      color: Number(credit?.processing_errors) ? "bad" : "",
    },
  ];
  return `<div class="metrics">${cards.map((card) => `<div class="metric"><div class="metric-label">${x(card.label)}</div><div class="metric-value ${card.color || ""}">${x(card.value)}</div><div class="metric-note">${x(card.note)}</div></div>`).join("")}</div>`;
}
function contextStatus() {
  const snapshot = caches.get(`${state.customer}/${state.month}`);
  return `<div id="account-metrics">${metricsHTML(snapshot)}</div><p class="section-note" id="snapshot-time">${snapshot ? `Snapshot fetched ${x(snapshot.fetched)}. Refresh after mutations or worker processing.` : "API data has not been loaded. Database links open live SQL independently."}</p>`;
}

function overviewHTML() {
  return (
    intro(
      "Billing, from receipt to invoice",
      "Inspect account state, submit real billing commands, and follow their effects into PostgreSQL.",
      '<button class="primary" data-refresh>↻ Refresh account</button>',
    ) +
    contextStatus() +
    `<div class="columns">${panel("Run the assignment", '<p class="section-note">One step at a time: grant credit, purchase an add-on, send usage, observe limits, and issue immutable invoices. Each step shows literal expected results.</p><a class="button primary" href="#assignment">▷ Open walkthrough</a>')}${panel("Inspect the financial trail", '<p class="section-note">Prepared SQL opens in a separate Adminer tab. Review the statement and press Execute after logging in.</p><div class="actions">' + dbButton("inbox", "Usage inbox") + dbButton("groups", "Accounting groups") + dbButton("credit", "Credit ledger") + "</div>")}</div>` +
    panel(
      "Recent requests",
      '<div id="recent-history">' +
        historyTable(client.history.slice(0, 6)) +
        "</div>",
    ) +
    notice(
      "This console operates on the current local database. Financial commands affect real stored demo data. Full assignment results require fresh seeded accounts and completed October/November 2026 UTC months.",
    )
  );
}
function customersHTML() {
  return (
    intro(
      "Customer directory",
      "The assignment seeds these two accounts. Customer discovery and creation are not exposed by the current API; live database details are available in Adminer.",
      dbButton("customers", "Live customer directory"),
    ) +
    `<section class="panel"><div class="panel-heading"><h3>Assignment seed directory</h3><span class="catalog-label">REFERENCE DATA · NOT A LIVE DB LIST</span></div><div class="table-scroll"><table><thead><tr><th>Customer</th><th>Country</th><th>Billing address</th><th></th></tr></thead><tbody>${seedCustomers.map((customer) => `<tr><td><div class="customer-name">${x(customer.name)}</div><div class="customer-meta mono">${x(customer.id)}</div></td><td>${x(customer.country)}</td><td>${x(customer.address)}</td><td><button class="small" data-customer="${customer.id}">Select account →</button></td></tr>`).join("")}</tbody></table></div></section>` +
    panel(
      "Use another existing customer",
      `<form id="custom-customer"><div class="form-grid">${field("customer_id", "Existing customer ID", "", { full: true, hint: "Selects an existing account; this does not create a customer." })}</div><div class="actions"><button type="submit">Select customer</button></div></form>`,
    )
  );
}
function usageHTML() {
  const fields =
    field("source", "Producer source", state.source, { full: true }) +
    field("event_id", "Event ID", newKey()) +
    field("sandbox_id", "Sandbox ID", `${state.customer}-sandbox`) +
    field("metric", "Metric", "cpu_seconds") +
    field("units", "Integer units", "100000000", {
      hint: "Whole resource units, not millions. Exact int64 values are preserved.",
    }) +
    field(
      "period_start",
      "Period start · explicit UTC/offset",
      "2026-10-10T12:00:00Z",
      { full: true },
    ) +
    field(
      "period_end",
      "Period end · explicit UTC/offset",
      "2026-10-10T13:00:00Z",
      { full: true },
    );
  return (
    intro(
      "Submit sandbox measurements",
      "The platform sends increments. Keep source, event ID and every field unchanged on retry. Receipt acceptance does not yet confirm accounting.",
      dbButton("inbox", "Inspect receipts"),
    ) +
    `<div class="columns">${operationForm("usage", "Single measurement", fields, "Send measurement")}${panel("Batch editor", `<form id="batch-form"><label class="field">Request body<textarea name="batch" rows="18" spellcheck="false">${x(JSON.stringify({ events: [] }, null, 2))}</textarea><small>Paste a complete {"events": [...]} body. Numeric tokens are sent without converting them to JavaScript numbers.</small></label><div class="actions"><button class="primary" type="submit">Send batch</button><button type="button" id="copy-measurement">Use measurement form</button><button type="button" id="load-late">Load late October example</button></div><div data-form-result class="inline-result"></div></form>`)}</div>` +
    panel(
      "Worker visibility",
      '<div class="actions"><button data-refresh>↻ Refresh accounting</button>' +
        dbButton("pending", "Pending / errors") +
        dbButton("ratings", "Receipt → group") +
        "</div>" +
        contextStatus(),
    ) +
    notice(
      "Intervals must fit one UTC month and one applicable price version. A late event uses its consumption-time price; after closure, the worker routes it to an eligible open billing month.",
    )
  );
}
function pricesHTML() {
  const fields =
    select("scope", "Price scope", [
      { value: "default", label: "Default · all customers" },
      { value: "customer", label: `Override · ${state.customer}` },
    ]) +
    field("metric", "Metric", "cpu_seconds") +
    field("price", "USD per 1_000_000 units", "0.05") +
    field("effective_from", "Effective date · UTC", now().slice(0, 10), {
      type: "date",
      full: true,
      hint: "Today or a future UTC date. Applies from 00:00 UTC; already rated usage is protected.",
    }) +
    field("idempotency_key", "Operation key", newKey(), { full: true });
  return (
    intro(
      "Historical prices",
      "Customer-specific prices take precedence over defaults. New ordinary versions start at midnight UTC on today or a future date and preserve already rated history.",
      dbButton("prices", "Live price versions"),
    ) +
    `<div class="columns">${operationForm("prices", "Create price version", fields, "Create price")}${panel("Assignment seed prices", `<p class="catalog-label">REFERENCE DATA · NOT THE CURRENT CATALOG</p><div class="table-scroll"><table><thead><tr><th>Scope</th><th>USD / million</th><th>Effective · UTC</th></tr></thead><tbody><tr><td>default</td><td>$0.05</td><td class="mono">2026-10-01</td></tr><tr><td>default</td><td>$0.06</td><td class="mono">2026-10-15</td></tr><tr><td>acme override</td><td>$0.04</td><td class="mono">2026-10-01</td></tr></tbody></table></div><p class="hint">Use the live database link to see added versions and exact timestamps. The API generates each new price ID.</p>`)}</div>` +
    notice(
      "Supply effective_from as YYYY-MM-DD. An exact retry reuses the same key and original date.",
    )
  );
}
function creditHTML() {
  const fields =
    field("amount", "Grant amount · USD", "25.00") +
    field("idempotency_key", "Operation key", newKey(), { full: true });
  return (
    intro(
      "Grant credit and inspect its use",
      "E2B grants credit. It covers usage only; add-ons remain payable. New grants do not rewrite previous allocations.",
      "<button data-refresh>↻ Refresh balance</button>" +
        dbButton("credit", "Credit ledger"),
    ) +
    contextStatus() +
    `<div class="columns">${operationForm("credits", "E2B credit grant", fields, "Grant credit")}${panel("Exact balance", '<p class="section-note">The summary rounds for display. The exact eight-decimal USD amount and API tick string stay visible. Credit allocation happens in the worker, before cent rounding.</p><div class="actions">' + dbButton("credit", "View grants and debits") + dbButton("groups", "View allocations") + "</div>")}</div>`
  );
}
function addonsHTML() {
  const fields =
    field("addon_name", "Add-on name", "concurrency_pack") +
    field("purchased_at", "Purchase date · UTC", now().slice(0, 10), { type: "date" }) +
    field("idempotency_key", "Operation key", newKey(), { full: true });
  return (
    intro(
      "Recurring add-ons",
      "A purchase snapshots the monthly price. The full price applies from the purchase UTC month; credit and spend limits do not cover add-ons.",
      dbButton("addons", "Live catalog / subscriptions"),
    ) +
    `<div class="columns">${operationForm("addons", "Purchase for selected customer", fields, "Purchase add-on")}${panel("Assignment add-on", '<p class="catalog-label">SEED REFERENCE</p><h2>concurrency_pack</h2><p class="metric-value">$20.00 <span class="subtle" style="font-size:12px">/ month</span></p><p class="hint">One subscription per customer and add-on. Billing derives its ID. Use the live database link for current purchase state.</p>')}</div>`
  );
}
function limitsHTML() {
  const fields =
    field("limit", "Monthly limit · USD", "15.00") +
    `<label class="checkbox-field"><input name="unlimited" type="checkbox">No spending limit (null)</label>` +
    field("idempotency_key", "Operation key", newKey(), { full: true });
  return (
    intro(
      "Budget signal for the platform",
      "The limit compares gross consumption-month usage, before credit and without add-ons. Reaching it does not stop accounting.",
      "<button data-refresh>↻ Refresh selected month</button>" +
        dbButton("limits", "Usage and limit history"),
    ) +
    contextStatus() +
    `<div class="columns">${operationForm("spend-limit", "Set or increase monthly limit", fields, "Set limit")}${panel("Platform status", '<p class="section-note">Read the selected UTC month or let the server select its current month. State includes unresolved receipt counts, so acceptance alone is not treated as updated spending.</p><div class="actions"><button data-read-current>Read current month</button><button data-read-month>Read selected month</button></div><div id="limit-result" class="inline-result"></div>')}</div>` +
    notice(
      "A zero limit is a real zero budget. Unlimited sends null. Raising the limit can return the customer below it; an identical retry of an older command does not undo a newer setting.",
    )
  );
}
function invoicesHTML() {
  const eligible = invoiceEligible(state.month, businessTime(runtime));
  const fields = field("month", "Billing month · UTC", state.month, {
    type: "month",
    full: true,
  });
  return (
    intro(
      "Monthly invoice",
      "One immutable invoice per customer and completed UTC month. Closing snapshots processed usage without draining the inbox.",
      dbButton("invoices", "Issued snapshots"),
    ) +
    (!eligible
      ? notice(
          `The displayed clock suggests ${state.month} has not ended. The API decides eligibility and may return 422. The fixed-clock invoice demo uses 2026-11-01T00:00:00Z; see the walkthrough guide for the two-phase assignment.`,
        )
      : "") +
    `<div class="columns">${operationForm("invoices", "Issue or retry monthly invoice", fields, "Issue invoice")}${panel("Read an issued invoice", '<p class="section-note">Retrieve the selected customer\'s snapshot for the Billing month entered in the invoice form. Issue and Load use the same month. GET has no financial effects.</p><div class="actions"><button class="primary" id="read-invoice">Load invoice</button><button id="print-invoice">Print loaded invoice</button></div><p id="invoice-load-status" class="section-note" role="status" aria-live="polite"></p>')}</div>` +
    `<div id="invoice-output">${empty("No invoice loaded", "Load an issued invoice or issue a completed month to display its stored lines.")}</div>`
  );
}
function invoiceHTML(invoice) {
  return panel(
    "Issued snapshot",
    `<h2 class="invoice-title">${x(invoice.number)}</h2><div class="invoice-meta"><div><strong>${x(invoice.customer_name)}</strong><br>${x(invoice.billing_address)}<br>${x(invoice.country)}</div><div>Month: ${x(invoice.month)} · ${x(invoice.currency)}<br>Issued: ${x(invoice.issued_at)}<br>Exact usage: ${x(formatTicks(invoice.gross_usage_ticks, true))}<br>Exact credit: ${x(formatTicks(invoice.credit_used_ticks, true))}</div></div><div class="table-scroll"><table><thead><tr><th>Kind</th><th>Description</th><th>Usage month</th><th>Units</th><th>Amount</th></tr></thead><tbody>${invoice.lines.map((line) => `<tr><td>${x(line.kind)}</td><td>${x(line.description)}${line.price_version_id ? `<div class="customer-meta mono">${x(line.price_version_id)}</div>` : ""}</td><td>${x(line.usage_month || "—")}</td><td class="number">${x(line.units ? groupDigits(line.units) : "—")}</td><td class="number">${x(formatCents(line.amount_cents))}</td></tr>`).join("")}</tbody></table></div><div class="invoice-total"><strong>Total</strong><strong>${x(formatCents(invoice.total_cents))}</strong></div><details><summary>Immutable API snapshot and exact audit values</summary><pre>${x(pretty(invoice))}</pre></details>`,
  );
}
function databaseHTML() {
  return (
    intro(
      "Inspect PostgreSQL",
      "Open a prepared statement in another tab. Log in to Adminer, then press Execute. Every query uses the selected account and UTC month where relevant.",
      `<a class="button" target="_blank" rel="noopener" href="${x(adminerURL())}">↗ Open Adminer</a>`,
    ) +
    notice(
      "Connection: PostgreSQL · server postgres · database e2b_billing · user e2b. Use the configured local database password. After first login, reopen the GUI query link if Adminer redirected to the database overview.",
    ) +
    `<div class="query-grid">${inspectionViews.map((view) => `<article class="query-card"><h3>${x(view.title)}</h3><p>${x(view.description)}</p><div class="actions">${dbButton(view.id, "Open SQL")}<button class="small" data-query="${view.id}">Preview</button></div></article>`).join("")}</div><div id="query-preview" style="margin-top:24px"></div>`
  );
}
function serviceHTML() {
  return (
    intro(
      "Local service operations",
      "Prepared SQL is reviewed and executed manually in Adminer. Browser state and database financial state are separate.",
      dbButton("pending", "Investigate pending / errors"),
    ) +
    `<div class="columns">${panel(
      "Prepare a scoped maintenance statement",
      `<form id="maintenance-form"><div class="form-grid">${field("source", "Exact GUI producer source", state.source, { full: true, hint: "Only demo-ui/ sources qualify. No financial rows are deleted." })}${field("event_id", "Exact event ID", "", { full: true, required: false, hint: "Required to release one quarantined receipt. Optional for pending cleanup." })}${select(
        "action",
        "Maintenance action",
        [
          { value: "delete-pending", label: "Delete unprocessed GUI receipts" },
          { value: "release-error", label: "Release one repaired error" },
        ],
      )}</div><div class="actions"><button type="submit">Prepare SQL</button></div></form><div id="maintenance-preview" class="inline-result"></div>`,
    )}${panel("Browser checkpoints", '<p class="section-note">Export request history before clearing local state. Clearing the journal does not reset credit, invoices, add-ons, or any other database state.</p><div class="actions"><button data-export>Export request journal</button><button id="reset-assignment">Reset walkthrough cursor</button><button class="danger" id="clear-history">Clear local journal</button></div><p class="hint">For a fresh assignment run, use a separate Compose project with an empty volume, migrate it, then open that portal. The console cannot stop containers or safely reset financial history.</p><a class="link" href="/guides/demo-console/#fresh-assignment-data" target="_blank" rel="noopener">Fresh demo environment instructions ↗</a>')}</div>`
  );
}
function historyTable(records) {
  if (!records.length)
    return empty(
      "No saved requests",
      "Requests are saved before sending so unknown outcomes can be retried with exactly the same content.",
    );
  return `<div class="table-scroll"><table><thead><tr><th>Time · UTC</th><th>Request</th><th>Status</th><th>Actions</th></tr></thead><tbody>${records.map((record) => `<tr><td class="mono">${x(record.started.slice(11, 19))}</td><td><span class="history-path mono">${x(record.method)} ${x(record.path)}</span><div class="customer-meta">${x(record.label)}</div></td><td>${statusBadge(record.status)}</td><td><div class="actions"><button class="small" data-inspect="${x(record.id)}">Inspect</button><button class="small" data-retry="${x(record.id)}">Exact retry</button></div></td></tr>`).join("")}</tbody></table></div>`;
}
function historyHTML() {
  return (
    intro(
      "Request journal",
      "The latest 100 requests are stored in this browser before transmission. An exact retry preserves the original path, key, timestamps and JSON bytes.",
      "<button data-export>Export JSON</button>",
    ) +
    panel("Saved requests", historyTable(client.history)) +
    notice(
      "A timeout or disconnected browser can leave the commit outcome unknown. Retry the saved request; generating a new operation identity can duplicate its financial effect.",
    )
  );
}
function assignmentHTML() {
  if (!plan)
    return (
      intro(
        "Assignment walkthrough",
        `Loading the published ${state.walkthroughScenario} scenario…`,
      ) +
      empty(
        "Loading scenario",
        "The console uses the same inputs and expected outputs as the CLI simulator.",
      )
    );
  const progress = state.assignment.index;
  const index = selectedStep ?? Math.min(progress, plan.steps.length - 1);
  const step = plan.steps[index];
  const request = workflowRequest(step, state.assignment.source);
  const last = state.assignment.last;
  return (
    intro(
      plan.name === "billing-demo-invoice" ? "October invoice walkthrough" : "The complete billing assignment",
      "Advance one reviewed step at a time. Expected results are loaded from the published simulator fixture, not calculated by this GUI.",
      `<label>Scenario <select id="walkthrough-scenario" ${runner.running ? "disabled" : ""}><option value="billing-assignment" ${state.walkthroughScenario === "billing-assignment" ? "selected" : ""}>Complete assignment</option><option value="billing-demo-invoice" ${state.walkthroughScenario === "billing-demo-invoice" ? "selected" : ""}>October invoice with add-on</option></select></label>` +
      '<button class="primary" id="next-step" ' +
        (runner.running || progress >= plan.steps.length ? "disabled" : "") +
        ">▷ " +
        (progress >= plan.steps.length ? "Complete" : "Run next step") +
        '</button><button id="stop-step" ' +
        (!runner.running ? "disabled" : "") +
        ">Stop polling</button>",
    ) +
    notice(
      plan.name === "billing-demo-invoice"
        ? "Use fresh Cyberdyne seed data and E2B_DEMO_NOW=2026-11-01T00:00:00Z for both API and worker. Expected invoice: usage $6.17 + $12.00, add-on $20.00, total $38.17. Closing includes processed usage only."
        : "Use fresh seeded accounts and the original seed prices. With fixed clocks, run through step 18 at 2026-11-01T00:00:00Z, then restart API, worker and portal at 2026-12-01T00:00:00Z for step 19. Keep the database and this cursor. Late usage after closing follows its receipt month.",
    ) +
    `<p class="section-note">${progress} / ${plan.steps.length} verified steps · Source <code>${x(state.assignment.source)}</code> · Cursor survives reload. Lost-response steps send once, discard the observed success for demonstration, then repeat the same request.</p><div class="progress"><div style="width:${(progress / plan.steps.length) * 100}%"></div></div>` +
    `<div class="workflow-grid"><section class="panel"><ol class="workflow-list">${plan.steps.map((item, i) => `<li class="workflow-step ${i < progress ? "done" : i === progress ? "current" : ""}"><span class="step-number">${i < progress ? "✓" : i + 1}</span><div class="step-title"><button data-step="${i}">${x(item.name)}</button><div class="step-path">${x(item.request.method)} ${x(item.request.path)}</div></div></li>`).join("")}</ol></section><div>${panel(`Step ${index + 1} · ${step.name}`, `<p class="section-note">${x(request.method)} ${x(request.path)}${step.await ? " · polls for committed accounting" : ""}</p><h4 class="eyebrow">REQUEST</h4><pre>${x(request.body || "(no request body)")}</pre><h4 class="eyebrow" style="margin-top:22px">LITERAL EXPECTED RESULT</h4><pre>${x(pretty(step.want))}</pre>`)}${last ? panel("Last executed step", `<p class="section-note">${x(last.name)}</p>${notice(last.differences.length ? last.differences.join("\n") : "The declared expected result matched.", last.differences.length ? "error" : "success")}<pre>${x(pretty(last.actual))}</pre>`) : ""}</div></div>`
  );
}

async function render() {
  const generation = ++renderGeneration;
  const section =
    sections.find((item) => item.id === currentSection()) || sections[0];
  document.title = `E2B · ${section.title}`;
  $("#page-title").textContent = section.title;
  $("#section-role").textContent = section.role;
  $("#navigation").innerHTML = sections
    .map(
      (item) =>
        `<a href="#${item.id}" class="${item.id === section.id ? "active" : ""}" ${item.id === section.id ? 'aria-current="page"' : ""}><span class="nav-icon" aria-hidden="true">${item.icon}</span>${item.title}</a>`,
    )
    .join("");
  const views = {
    overview: overviewHTML,
    customers: customersHTML,
    usage: usageHTML,
    prices: pricesHTML,
    credit: creditHTML,
    addons: addonsHTML,
    limits: limitsHTML,
    invoices: invoicesHTML,
    database: databaseHTML,
    service: serviceHTML,
    history: historyHTML,
    assignment: assignmentHTML,
  };
  $("#content").innerHTML = views[section.id]();
  if (["overview", "usage", "credit", "limits"].includes(section.id)) {
    try {
      const snapshot = await refreshCustomer();
      if (generation !== renderGeneration) return;
      updateMetrics(snapshot);
    } catch (error) {
      if (generation === renderGeneration)
        $("#account-metrics").innerHTML = notice(error.message, "error");
    }
  }
}
function updateMetrics(snapshot) {
  if ($("#account-metrics"))
    $("#account-metrics").innerHTML = metricsHTML(snapshot);
  if ($("#snapshot-time"))
    $("#snapshot-time").textContent =
      `Snapshot fetched ${snapshot.fetched}. Refresh after mutations or worker processing.`;
}
function contextChanged() {
  state.customer = $("#customer").value;
  state.month = validMonth($("#month").value);
  saveState();
  render();
}
function selectCustomer(customer) {
  if (
    ![...$("#customer").options].some((option) => option.value === customer)
  ) {
    const option = document.createElement("option");
    option.value = customer;
    option.textContent = customer;
    $("#customer").append(option);
  }
  $("#customer").value = customer;
  contextChanged();
}
async function sendForm(form, request) {
  const resultContainer = form.querySelector("[data-form-result]");
  if (form.dataset.sending) return;
  form.dataset.sending = "true";
  const buttons = [...form.querySelectorAll("button")];
  buttons.forEach((button) => {
    button.disabled = true;
  });
  resultContainer.innerHTML =
    '<p class="loading">Sending the saved request…</p>';
  try {
    let result = await client.send(
      request,
      form.dataset.operation || "Usage batch",
    );
    if (
      form.querySelector('[name="lose_response"]')?.checked &&
      result.status >= 200 &&
      result.status < 300
    ) {
      result.record.simulatedLoss = true;
      result = await client.retry(result.record);
    }
    resultContainer.innerHTML = resultHTML(result);
    if (result.data?.lines && $("#invoice-output"))
      $("#invoice-output").innerHTML = invoiceHTML(result.data);
    if (result.status >= 200 && result.status < 300) {
      caches.clear();
      flash(
        result.status === 202
          ? "Receipt accepted. Refresh to observe accounting."
          : "Operation completed. Response is visible below and in the inspector.",
      );
    } else flash(result.data?.error?.message || result.text, true);
  } catch (error) {
    resultContainer.innerHTML = notice(error.message, "error");
  } finally {
    delete form.dataset.sending;
    buttons.forEach((button) => {
      button.disabled = false;
    });
  }
}
async function loadPlan() {
  const response = await fetch(`/simulator/${state.walkthroughScenario}.json`, {
    cache: "no-store",
  });
  if (!response.ok)
    throw new Error(
      `Scenario file could not be loaded: HTTP ${response.status}`,
    );
  plan = await response.json();
  if (!state.assignment)
    state.assignment = {
      source: state.source,
      index: 0,
      captures: {},
      completed: [],
      last: null,
    };
  runner = new AssignmentRunner({
    plan,
    state: state.assignment,
    client,
    save: (assignment) => {
      state.assignment = assignment;
      state.walkthroughs[state.walkthroughScenario] = assignment;
      saveState();
    },
    changed: () => {
      selectedStep = null;
    },
  });
  if (currentSection() === "assignment") render();
}
function exportJournal() {
  const blob = new Blob([JSON.stringify(state, null, 2)], {
    type: "application/json",
  });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = `e2b-demo-journal-${new Date().toISOString().slice(0, 10)}.json`;
  anchor.click();
  setTimeout(() => URL.revokeObjectURL(url), 1_000);
}
function showQuery(title, sql, target) {
  target.innerHTML = panel(
    title,
    `<pre>${x(sql)}</pre><div class="actions" style="margin-top:16px"><a class="button primary" target="_blank" rel="noopener" href="${x(adminerURL(sql))}">↗ Open in Adminer · execute manually</a><button data-copy-sql>Copy SQL</button></div>`,
  );
  target.scrollIntoView({ behavior: "smooth", block: "nearest" });
}

$("#content").addEventListener("change", async (event) => {
  if (event.target.name === "unlimited")
    event.target.form.querySelector('[name="limit"]').disabled = event.target.checked;
  if (event.target.id !== "walkthrough-scenario") return;
  try {
    if (runner?.running) throw new Error("Wait for the running scenario step to finish.");
    const scenario = event.target.value;
    if (!["billing-assignment", "billing-demo-invoice"].includes(scenario))
      throw new Error("Unknown walkthrough scenario.");
    state.walkthroughs[state.walkthroughScenario] = state.assignment;
    state.walkthroughScenario = scenario;
    state.assignment = state.walkthroughs[scenario] || null;
    plan = null;
    runner = null;
    selectedStep = null;
    render();
    saveState();
    await loadPlan();
  } catch (error) { describeError(error); }
});

$("#content").addEventListener("submit", async (event) => {
  const form = event.target;
  event.preventDefault();
  try {
    const values = Object.fromEntries(new FormData(form));
    if (form.dataset.operation) {
      await sendForm(
        form,
        buildCommand(form.dataset.operation, values, state.customer),
      );
      return;
    }
    if (form.id === "batch-form") {
      const parsed = parseExactJSON(values.batch);
      if (!parsed || !Array.isArray(parsed.events))
        throw new Error(
          "The batch must be an object containing an events array.",
        );
      await sendForm(form, {
        method: "POST",
        path: "/usage/batches",
        body: values.batch,
      });
      return;
    }
    if (form.id === "custom-customer") {
      selectCustomer(values.customer_id.trim());
      return;
    }
    if (form.id === "maintenance-form") {
      showQuery(
        "Prepared maintenance · manual execution",
        maintenanceSQL(values.action, {
          source: values.source,
          eventId: values.event_id,
        }),
        $("#maintenance-preview"),
      );
      return;
    }
  } catch (error) {
    describeError(error);
  }
});
$("#content").addEventListener("click", async (event) => {
  const button = event.target.closest("button");
  if (!button || (button.type === "submit" && button.form)) return;
  try {
    if (button.hasAttribute("data-new-key")) {
      button.form.querySelector('[name="idempotency_key"]').value = newKey();
      flash("A new operation key is ready. This represents a new command.");
      return;
    }
    if (button.hasAttribute("data-preview")) {
      const request = buildCommand(
        button.form.dataset.operation,
        Object.fromEntries(new FormData(button.form)),
        state.customer,
      );
      button.form.querySelector("[data-form-result]").innerHTML =
        `<p class="section-note">${x(request.method)} ${x(request.path)}</p><pre>${x(request.body)}</pre>`;
      return;
    }
    if (button.dataset.customer) {
      selectCustomer(button.dataset.customer);
      location.hash = "overview";
      return;
    }
    if (button.hasAttribute("data-refresh")) {
      button.disabled = true;
      try {
        updateMetrics(await refreshCustomer());
      } finally {
        button.disabled = false;
      }
      return;
    }
    if (button.dataset.inspect) {
      const record = client.history.find(
        (item) => item.id === button.dataset.inspect,
      );
      $("#request-detail").innerHTML = requestDetails(record);
      $("#inspector-content").hidden = false;
      $("#toggle-inspector").setAttribute("aria-expanded", "true");
      $("#inspector-content").scrollIntoView({
        behavior: "smooth",
        block: "nearest",
      });
      return;
    }
    if (button.dataset.retry) {
      const record = client.history.find(
        (item) => item.id === button.dataset.retry,
      );
      button.disabled = true;
      try {
        const result = await client.retry(record);
        flash(
          `Exact retry completed: ${result.status}`,
          result.status < 200 || result.status >= 300,
        );
      } finally {
        button.disabled = false;
      }
      if (currentSection() === "history") render();
      return;
    }
    if (button.dataset.query) {
      const view = inspectionViews.find(
        (item) => item.id === button.dataset.query,
      );
      showQuery(view.title, inspectionSQL(view.id, state), $("#query-preview"));
      return;
    }
    if (button.hasAttribute("data-copy-sql")) {
      await navigator.clipboard.writeText(
        button.closest(".panel").querySelector("pre").textContent,
      );
      flash("SQL copied.");
      return;
    }
    if (button.hasAttribute("data-export")) {
      exportJournal();
      return;
    }
    if (button.id === "copy-measurement") {
      const form = $('#content form[data-operation="usage"]');
      $("#batch-form textarea").value = buildCommand(
        "usage",
        Object.fromEntries(new FormData(form)),
        state.customer,
      ).body;
      flash("Measurement copied into the batch editor.");
      return;
    }
    if (button.id === "load-late") {
      const fields = {
        source: state.source,
        event_id: newKey(),
        sandbox_id: `${state.customer}-sandbox`,
        metric: "cpu_seconds",
        period_start: "2026-10-30T12:00:00Z",
        period_end: "2026-10-30T13:00:00Z",
        units: "50000000",
      };
      $("#batch-form textarea").value = buildCommand(
        "usage",
        fields,
        state.customer,
      ).body;
      flash("Late October example loaded; sending remains explicit.");
      return;
    }
    if (
      button.hasAttribute("data-read-current") ||
      button.hasAttribute("data-read-month")
    ) {
      const path = customerPath(
        state.customer,
        button.hasAttribute("data-read-current")
          ? "limit-status"
          : `months/${state.month}/limit-status`,
      );
      button.disabled = true;
      try {
        const result = await client.send(
          { method: "GET", path },
          "Platform limit status",
        );
        $("#limit-result").innerHTML = resultHTML(result);
      } finally {
        button.disabled = false;
      }
      return;
    }
    if (button.id === "read-invoice") {
      const month = validMonth(
        $('#content form[data-operation="invoices"] [name="month"]').value,
      );
      const customer = state.customer;
      const output = $("#invoice-output");
      const status = $("#invoice-load-status");

      button.disabled = true;
      button.textContent = "Loading…";
      status.textContent = `Loading invoice for ${customer} · ${month}…`;
      try {
        const result = await client.send(
          {
            method: "GET",
            path: customerPath(customer, `invoices/${month}`),
          },
          "Read issued invoice",
        );
        output.innerHTML =
          result.status === 200
            ? invoiceHTML(result.data)
            : panel("Invoice response", resultHTML(result));
        status.textContent =
          result.status === 200
            ? `Invoice ${result.data.number} for ${result.data.month} loaded.`
            : result.data?.error?.message ||
              "Invoice could not be loaded. See the response below.";
      } catch (error) {
        status.textContent = error.message;
        throw error;
      } finally {
        button.disabled = false;
        button.textContent = "Load invoice";
      }
      return;
    }
    if (button.id === "print-invoice") {
      if (!$("#invoice-output .invoice-title"))
        throw new Error("Load an issued invoice first.");
      window.print();
      return;
    }
    if (button.dataset.step !== undefined) {
      selectedStep = Number(button.dataset.step);
      render();
      return;
    }
    if (button.id === "next-step") {
      button.disabled = true;
      $("#stop-step").disabled = false;
      const result = await runner.step();
      render();
      flash(
        result.differences.length
          ? "Expected result did not match. The cursor remains on this step; inspect the difference before retrying."
          : "Step matched its declared expected result.",
        result.differences.length > 0,
      );
      return;
    }
    if (button.id === "stop-step") {
      runner.stop();
      button.disabled = true;
      flash(
        "Polling will stop after the current request. The saved step can be retried.",
      );
      return;
    }
    if (button.id === "reset-assignment") {
      if (runner?.running)
        throw new Error("Wait for the running scenario step to finish.");
      if (
        !confirm(
          "Reset the walkthrough cursor only? Database data remains unchanged. Re-running requires fresh accounts or deliberate retries.",
        )
      )
        return;
      state.assignment = null;
      selectedStep = null;
      saveState();
      await loadPlan();
      flash("Walkthrough cursor reset. Database state was retained.");
      return;
    }
    if (button.id === "clear-history") {
      if (
        client.history.some((record) => record.status === "sending") ||
        runner?.running
      )
        throw new Error(
          "Wait for in-flight requests before clearing the journal.",
        );
      if (
        !confirm(
          "Clear this browser request journal? Export it first if you need original request identities. Database state remains unchanged.",
        )
      )
        return;
      client.history = [];
      state.history = [];
      saveState();
      flash("Local journal cleared. No database rows were changed.");
      return;
    }
  } catch (error) {
    describeError(error);
    if (button.id === "next-step") render();
  }
});
$("#toggle-inspector").addEventListener("click", () => {
  const content = $("#inspector-content");
  content.hidden = !content.hidden;
  $("#toggle-inspector").setAttribute("aria-expanded", String(!content.hidden));
  if (!lastRecord)
    $("#request-detail").innerHTML = empty(
      "No request selected",
      "Send a command or inspect a saved request.",
    );
});
$("#customer").addEventListener("change", () => {
  try {
    contextChanged();
  } catch (error) {
    describeError(error);
  }
});
$("#month").addEventListener("change", () => {
  try {
    contextChanged();
  } catch (error) {
    describeError(error);
  }
});
window.addEventListener("hashchange", () => {
  selectedStep = null;
  render();
});
async function health() {
  try {
    const response = await fetch("/api/healthz", {
      cache: "no-store",
      signal: AbortSignal.timeout(5_000),
    });
    $("#api-health").textContent = response.ok
      ? "● API ONLINE"
      : `API ${response.status}`;
    $("#api-health").className = `badge ${response.ok ? "green" : "red"}`;
  } catch {
    $("#api-health").textContent = "● API OFFLINE";
    $("#api-health").className = "badge red";
  }
}
async function refreshRuntime() {
  try {
    const next = await loadRuntime();
    const changed = JSON.stringify(runtime) !== JSON.stringify(next);
    runtime = next;
    tick();
    if (changed) render();
  } catch {
    runtime = null;
    tick();
  }
}
function tick() {
  const mode = runtime?.clock_mode === "fixed"
    ? "SIMULATED UTC"
    : runtime ? "BROWSER UTC" : "BROWSER UTC · metadata unavailable";
  $("#utc-clock").textContent = `${mode} ${now().slice(0, 19).replace("T", " ")}`;
}
if (
  ![...$("#customer").options].some((option) => option.value === state.customer)
) {
  const option = document.createElement("option");
  option.value = state.customer;
  option.textContent = state.customer;
  $("#customer").append(option);
}
$("#customer").value = state.customer;
$("#month").value = state.month;
for (const record of client.history)
  if (record.status === "sending") {
    record.status = "unknown";
    record.response =
      "Browser restarted before recording an outcome. Retry the original request.";
  }
render();
health();
tick();
loadPlan().catch(describeError);
refreshRuntime();
setInterval(health, HEALTH_POLL_MS);
setInterval(refreshRuntime, HEALTH_POLL_MS);
setInterval(tick, 1_000);
if (storageFailure)
  flash(`Saved browser state could not be loaded: ${storageFailure}.`, true);
