import { parseExactJSON } from "./core.mjs";

export const REQUEST_TIMEOUT_MS = 40_000;
export const HISTORY_LIMIT = 100;

// Persist the original request before sending; retries always use these bytes.
export class APIClient {
  constructor({
    fetcher = globalThis.fetch.bind(globalThis),
    history = [],
    save,
    changed = () => {},
  }) {
    this.fetcher = fetcher;
    this.history = history;
    this.save = save;
    this.changed = changed;
  }

  async send(request, label = "Manual request", retryOf = null) {
    if (
      !/^(GET|POST)$/.test(request.method) ||
      !request.path.startsWith("/") ||
      request.path.startsWith("//")
    )
      throw new Error("Use a local API path and GET or POST.");
    const record = {
      id: crypto.randomUUID(),
      label,
      method: request.method,
      path: request.path,
      body: request.body ?? null,
      retryOf,
      started: new Date().toISOString(),
      status: "sending",
      response: null,
    };
    const previous = this.history;
    this.history = [record, ...previous].slice(0, HISTORY_LIMIT);
    try {
      this.save(this.history);
    } catch (error) {
      this.history = previous;
      throw new Error(
        `Request was not sent: browser storage is unavailable (${error.message}). Export or clear the local journal and try again.`,
      );
    }
    this.changed(record);

    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
    let result;
    try {
      const response = await this.fetcher(`/api${request.path}`, {
        method: request.method,
        headers:
          request.body == null ? {} : { "Content-Type": "application/json" },
        body: request.body ?? undefined,
        signal: controller.signal,
        cache: "no-store",
      });
      const text = await response.text();
      record.status = response.status;
      record.response = text;
      record.retryAfter = response.headers.get("Retry-After");
      let data = null;
      try {
        data = text ? parseExactJSON(text) : null;
      } catch {
        /* Keep non-JSON transport errors visible. */
      }
      result = { status: response.status, data, text, record };
    } catch (error) {
      record.status = "unknown";
      record.response = `${error.message}. The commit outcome may be unknown; retry the original request.`;
      result = { status: 0, data: null, text: record.response, record };
    } finally {
      clearTimeout(timeout);
      record.finished = new Date().toISOString();
    }

    try {
      this.save(this.history);
    } catch {
      record.storageWarning =
        "Response checkpoint could not be saved. The original request remains available for retry.";
    }
    this.changed(record);
    return result;
  }

  retry(record) {
    return this.send(
      { method: record.method, path: record.path, body: record.body },
      `Retry: ${record.label}`,
      record.id,
    );
  }
}
