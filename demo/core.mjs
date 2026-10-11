// Transport and presentation helpers keep API integers exact in the browser.
export const INT64_MAX = 9_223_372_036_854_775_807n;
const MAX_SAFE_INTEGER = 9_007_199_254_740_991n;
const TICKS_PER_CENT = 1_000_000n;
const TICKS_PER_DOLLAR = 100_000_000n;

export function integer(value, label, minimum = 0n) {
  const text = String(value).trim();
  if (!/^[0-9]+$/.test(text))
    throw new Error(`${label}: enter a whole non-negative integer.`);
  const number = BigInt(text);
  if (number < minimum || number > INT64_MAX)
    throw new Error(`${label}: value is outside the supported int64 range.`);
  return number;
}

export function usdToCents(value, minimum = 0n) {
  const text = String(value).trim();
  if (!/^\d+(\.\d{1,2})?$/.test(text))
    throw new Error(
      "Enter USD with at most two decimal places, for example 25.00.",
    );
  const [dollars, fraction = ""] = text.split(".");
  return integer(
    (BigInt(dollars) * 100n + BigInt(fraction.padEnd(2, "0"))).toString(),
    "USD amount",
    minimum,
  );
}

export function formatCents(value) {
  const amount = BigInt(value);
  const absolute = amount < 0n ? -amount : amount;
  return `${amount < 0n ? "−" : ""}$${groupDigits(absolute / 100n)}.${String(absolute % 100n).padStart(2, "0")}`;
}

export function formatTicks(value, exact = false) {
  const amount = BigInt(value);
  const absolute = amount < 0n ? -amount : amount;
  if (!exact)
    return formatCents(
      (amount < 0n ? -1n : 1n) *
        ((absolute + TICKS_PER_CENT / 2n) / TICKS_PER_CENT),
    );
  return `${amount < 0n ? "−" : ""}$${groupDigits(absolute / TICKS_PER_DOLLAR)}.${String(absolute % TICKS_PER_DOLLAR).padStart(8, "0")}`;
}

export function groupDigits(value) {
  return String(value).replace(/\B(?=(\d{3})+(?!\d))/g, "_");
}

// Quote unsafe integer tokens before JSON.parse; strings and decimals stay intact.
export function parseExactJSON(text) {
  // Validate syntax before protecting large tokens; malformed JSON must stay invalid.
  JSON.parse(text);
  const protectedText = text.replace(
    /"(?:[^"\\]|\\.)*"|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/g,
    (token) => {
      if (token.startsWith('"') || /[.eE]/.test(token)) return token;
      const value = BigInt(token);
      return value > MAX_SAFE_INTEGER || value < -MAX_SAFE_INTEGER
        ? JSON.stringify(token)
        : token;
    },
  );
  return JSON.parse(protectedText);
}

// BigInt fields are emitted as JSON integer tokens, never as quoted numbers.
export function wireJSON(value, indentation = 2) {
  function encode(input, depth) {
    if (typeof input === "bigint") return input.toString();
    if (input === null || typeof input !== "object") {
      if (typeof input === "number" && !Number.isSafeInteger(input))
        throw new Error("Unsafe JSON number; use an exact integer value.");
      return JSON.stringify(input);
    }
    const array = Array.isArray(input);
    const entries = array
      ? input.map((item) => encode(item, depth + 1))
      : Object.entries(input).map(
          ([key, item]) => `${JSON.stringify(key)}: ${encode(item, depth + 1)}`,
        );
    if (!entries.length) return array ? "[]" : "{}";
    const pad = " ".repeat(indentation * (depth + 1));
    return `${array ? "[" : "{"}\n${pad}${entries.join(`,\n${pad}`)}\n${" ".repeat(indentation * depth)}${array ? "]" : "}"}`;
  }
  return encode(value, 0);
}

export function validMonth(value) {
  if (!/^[1-9]\d{3}-(0[1-9]|1[0-2])$/.test(value))
    throw new Error(
      "Use a UTC month as YYYY-MM, with a year from 1000 through 9999.",
    );
  return value;
}

export function utcTimestamp(value) {
  const text = String(value).trim();
  const match =
    /^([1-9]\d{3})-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,6})?(Z|[+-]\d{2}:\d{2})$/.exec(
      text,
    );
  if (!match || !Number.isFinite(Date.parse(text)))
    throw new Error(
      "Use a valid RFC3339 timestamp with an explicit offset and at most six fractional digits.",
    );
  // Keep the original microseconds and offset; the API owns full calendar validation.
  return text;
}

export function timestampMicroseconds(value) {
  const text = utcTimestamp(value);
  const fraction = /\.(\d+)/.exec(text)?.[1] || "";
  return (
    BigInt(Date.parse(text)) * 1_000n + BigInt(fraction.padEnd(6, "0").slice(3))
  );
}

export function customerPath(customer, suffix) {
  return `/customers/${encodeURIComponent(customer)}/${suffix}`;
}

export function buildCommand(operation, fields, customer) {
  const key = String(fields.idempotency_key || "");
  if (
    ["prices", "credits", "addons", "spend-limit"].includes(operation) &&
    !key
  )
    throw new Error(
      "An idempotency key is required. Keep it unchanged when retrying.",
    );
  if (operation === "prices")
    return {
      method: "POST",
      path: "/prices",
      body: wireJSON({
        idempotency_key: key,
        customer_id: fields.scope === "default" ? null : customer,
        metric: fields.metric,
        price_per_million_cents: usdToCents(fields.price),
        effective_from: calendarDate(fields.effective_from),
      }),
    };
  if (operation === "credits")
    return {
      method: "POST",
      path: customerPath(customer, "credits"),
      body: wireJSON({
        idempotency_key: key,
        amount_cents: usdToCents(fields.amount, 1n),
      }),
    };
  if (operation === "addons")
    return {
      method: "POST",
      path: customerPath(customer, "addons"),
      body: wireJSON({
        idempotency_key: key,
        addon_name: fields.addon_name,
        purchased_at: calendarDate(fields.purchased_at),
      }),
    };
  if (operation === "spend-limit")
    return {
      method: "POST",
      path: customerPath(customer, "spend-limit"),
      body: wireJSON({
        idempotency_key: key,
        limit_cents:
          fields.unlimited === "on" ? null : usdToCents(fields.limit),
      }),
    };
  if (operation === "invoices")
    return {
      method: "POST",
      path: customerPath(customer, "invoices"),
      body: wireJSON({ month: validMonth(fields.month) }),
    };
  if (operation === "usage") {
    const start = utcTimestamp(fields.period_start);
    const end = utcTimestamp(fields.period_end);
    if (timestampMicroseconds(end) <= timestampMicroseconds(start))
      throw new Error("The measurement end must be after its start.");
    return {
      method: "POST",
      path: "/usage/batches",
      body: wireJSON({
        events: [
          {
            schema_version: 1,
            source: fields.source,
            event_id: fields.event_id,
            customer_id: customer,
            sandbox_id: fields.sandbox_id,
            metric: fields.metric,
            period_start: start,
            period_end: end,
            units: integer(fields.units, "Units"),
          },
        ],
      }),
    };
  }
  throw new Error(`Unknown operation: ${operation}`);
}

export function expectationDifferences(actual, expected, path = "response") {
  if (expected === null || typeof expected !== "object") {
    return actual === expected
      ? []
      : [
          `${path}: got ${JSON.stringify(actual)}, expected ${JSON.stringify(expected)}`,
        ];
  }
  if (actual === null || typeof actual !== "object")
    return [`${path}: expected an object or array`];
  const errors = [];
  if (
    Array.isArray(expected) &&
    (!Array.isArray(actual) || actual.length !== expected.length)
  )
    errors.push(`${path}: expected ${expected.length} rows`);
  for (const [key, value] of Object.entries(expected))
    errors.push(
      ...expectationDifferences(actual[key], value, `${path}.${key}`),
    );
  return errors;
}

export function invoiceEligible(month, now = new Date()) {
  validMonth(month);
  // Lexical UTC month comparison also handles the upper supported year boundary.
  return month < now.toISOString().slice(0, 7);
}

// calendarDate rejects timestamps and invalid dates before constructing a billing command.
export function calendarDate(value) {
  if (!/^[0-9]{4}-[0-9]{2}-[0-9]{2}$/.test(value) || Number(value.slice(0, 4)) < 1000)
    throw new Error("Use a valid calendar date in YYYY-MM-DD format, years 1000 through 9999.");
  const parsed = new Date(`${value}T00:00:00Z`);
  if (!Number.isFinite(parsed.getTime()) || parsed.toISOString().slice(0, 10) !== value)
    throw new Error("Use a valid calendar date in YYYY-MM-DD format.");
  return value;
}
