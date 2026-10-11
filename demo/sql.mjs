import { validMonth } from "./core.mjs";

// Explicit PostgreSQL escape literals work independently of session string settings.
export function sqlLiteral(value) {
  if (String(value).includes("\0"))
    throw new Error("SQL parameters cannot contain a NUL character.");
  return `E'${String(value).replaceAll("\\", "\\\\").replaceAll("'", "''")}'`;
}

export const inspectionViews = [
  {
    id: "customers",
    title: "Customers and account state",
    description:
      "Customer details, exact credit, limits and next invoice number.",
  },
  {
    id: "inbox",
    title: "Received usage",
    description:
      "Measurements for the selected customer and consumption month.",
  },
  {
    id: "pending",
    title: "Pending and quarantined",
    description:
      "Unprocessed receipts, including errors requiring investigation.",
  },
  {
    id: "ratings",
    title: "Receipt → accounting group",
    description: "Trace each measurement to its price and billing month.",
  },
  {
    id: "groups",
    title: "Rated usage groups",
    description:
      "Exact gross and allocated credit, with late-usage attribution.",
  },
  {
    id: "prices",
    title: "Historical price versions",
    description: "Default and customer prices; effective times stay visible.",
  },
  {
    id: "credit",
    title: "Credit ledger",
    description: "Grant and debit audit trail in exact microcents.",
  },
  {
    id: "addons",
    title: "Add-on catalog and subscriptions",
    description: "Monthly prices and customer purchase snapshots.",
  },
  {
    id: "limits",
    title: "Monthly gross usage and limit history",
    description: "Consumption-month spending before credit, excluding add-ons.",
  },
  {
    id: "invoices",
    title: "Issued invoice snapshots",
    description: "Immutable documents and their frozen usage groups.",
  },
  {
    id: "operations",
    title: "Command retry records",
    description:
      "Stored price and purchase requests and their original responses.",
  },
  {
    id: "migrations",
    title: "Migration versions",
    description: "Applied schema versions.",
  },
];

export function inspectionSQL(view, { customer, month }) {
  const c = sqlLiteral(customer);
  const m = `${sqlLiteral(validMonth(month) + "-01")}::date`;
  const period = `period_start >= (${m}::timestamp AT TIME ZONE 'UTC')\n  AND period_start < ((${m} + INTERVAL '1 month')::timestamp AT TIME ZONE 'UTC')`;
  const queries = {
    customers: `SELECT c.*, s.credit_balance_microcents, s.spend_limit_cents, s.state_version, s.next_invoice_number\nFROM customers c JOIN customer_billing_state s USING (customer_id)\nORDER BY c.customer_id;`,
    inbox: `SELECT source, event_id, sandbox_id, metric, period_start, period_end, units, received_at, processed_at, processing_error\nFROM usage_inbox\nWHERE customer_id = ${c} AND ${period}\nORDER BY received_at DESC, source, event_id\nLIMIT 200;`,
    pending: `SELECT * FROM usage_inbox\nWHERE customer_id = ${c} AND processed_at IS NULL\nORDER BY received_at, source, event_id\nLIMIT 200;`,
    ratings: `SELECT i.source, i.event_id, i.units, i.processed_at, r.group_id, g.price_version_id, g.usage_month, g.billing_month\nFROM usage_inbox i\nLEFT JOIN usage_ratings r USING (source, event_id)\nLEFT JOIN rated_usage_groups g USING (group_id)\nWHERE i.customer_id = ${c} AND ${period}\nORDER BY i.received_at, i.source, i.event_id\nLIMIT 200;`,
    groups: `SELECT * FROM rated_usage_groups\nWHERE customer_id = ${c} AND (usage_month = ${m} OR billing_month = ${m})\nORDER BY usage_month, billing_month, price_version_id;`,
    prices: `SELECT price_version_id, COALESCE(customer_id, 'default') AS scope, metric,\n       price_per_million_cents, price_per_million_cents / 100.0 AS usd_per_million, effective_from\nFROM price_versions\nWHERE customer_id IS NULL OR customer_id = ${c}\nORDER BY metric, customer_id NULLS FIRST, effective_from;`,
    credit: `SELECT credit_entry_id, group_id, amount_microcents, amount_microcents / 100000000.0 AS exact_usd, recorded_at\nFROM credit_entries WHERE customer_id = ${c}\nORDER BY recorded_at, credit_entry_id\nLIMIT 200;`,
    addons: `SELECT a.addon_name, a.monthly_price_cents AS catalog_price_cents, s.subscription_id, s.monthly_price_cents AS purchased_price_cents, s.purchased_at, s.start_month, c.cancelled_at, c.end_month\nFROM addons a LEFT JOIN addon_subscriptions s\n  ON s.addon_name = a.addon_name AND s.customer_id = ${c}\nLEFT JOIN addon_cancellations c USING (subscription_id)\nORDER BY a.addon_name;`,
    limits: `SELECT u.usage_month, u.gross_charge_microcents, s.spend_limit_cents\nFROM monthly_usage u JOIN customer_billing_state s USING (customer_id)\nWHERE u.customer_id = ${c}\nORDER BY u.usage_month;\n\nSELECT idempotency_key, response_payload->'limit_cents' AS limit_cents, created_at\nFROM api_idempotency_operations WHERE operation_scope = 'spend-limit/' || ${c}\nORDER BY created_at, idempotency_key;`,
    invoices: `SELECT invoice_number, billing_month, total_cents, snapshot\nFROM invoices WHERE customer_id = ${c}\nORDER BY billing_month;\n\nSELECT * FROM invoiced_usage_groups\nWHERE customer_id = ${c} AND billing_month = ${m}\nORDER BY group_id;`,
    operations: `SELECT operation_scope, idempotency_key, request_payload, response_payload, created_at\nFROM api_idempotency_operations\nWHERE operation_scope = 'addons/' || ${c}\n   OR operation_scope = 'addon-cancellations/' || ${c}\n   OR operation_scope = 'credits/' || ${c}\n   OR operation_scope = 'spend-limit/' || ${c}\n   OR (operation_scope = 'prices' AND (request_payload->'price'->>'customer_id' = ${c}\n       OR request_payload->'price'->>'customer_id' = ''))\nORDER BY created_at DESC\nLIMIT 200;`,
    migrations: "TABLE schema_migrations;",
  };
  if (!queries[view]) throw new Error(`Unknown inspection view: ${view}`);
  return queries[view];
}

export function maintenanceSQL(action, { source, eventId }) {
  if (!source.startsWith("demo-ui/"))
    throw new Error("Maintenance is limited to an explicit demo-ui/ source.");
  const identity = `source = ${sqlLiteral(source)}${eventId ? ` AND event_id = ${sqlLiteral(eventId)}` : ""}`;
  const unrated =
    "NOT EXISTS (SELECT 1 FROM usage_ratings r WHERE r.source = i.source AND r.event_id = i.event_id)";
  if (action === "delete-pending")
    return `-- Preview first. Only this GUI source's unprocessed, error-free, unrated receipts qualify.\nSELECT source, event_id, customer_id, units FROM usage_inbox i\nWHERE ${identity} AND processed_at IS NULL AND processing_error IS NULL AND ${unrated};\n\n-- Stop the worker before executing this block if you need a stable preview.\nBEGIN;\nDELETE FROM usage_inbox i\nWHERE ${identity} AND processed_at IS NULL AND processing_error IS NULL AND ${unrated}\nRETURNING source, event_id;\nCOMMIT;`;
  if (action === "release-error" && eventId)
    return `-- Repair the underlying cause before release. Financial history is preserved.\nBEGIN;\nUPDATE usage_inbox i SET processing_error = NULL\nWHERE ${identity} AND processed_at IS NULL AND processing_error IS NOT NULL AND ${unrated}\nRETURNING source, event_id;\nCOMMIT;`;
  throw new Error(
    "Choose a supported maintenance action and an exact event ID for release.",
  );
}

export function adminerURL(sql = "", base = "/database/", connection = {}) {
  const parameters = new URLSearchParams({
    pgsql: connection.server || "postgres",
    username: connection.username || "e2b",
    db: connection.database || "e2b_billing",
    ns: "public",
    sql,
  });
  return `${base}?${parameters}`;
}
