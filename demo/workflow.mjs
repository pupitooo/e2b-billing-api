import { expectationDifferences } from "./core.mjs";

const ACCOUNTING_ATTEMPTS = 20;
const ACCOUNTING_POLL_MS = 1_000;

export function workflowRequest(step, source) {
  function substitute(value) {
    if (typeof value === "string")
      return value.replaceAll("{{source}}", source);
    if (Array.isArray(value)) return value.map(substitute);
    if (value && typeof value === "object")
      return Object.fromEntries(
        Object.entries(value).map(([key, item]) => [key, substitute(item)]),
      );
    return value;
  }
  return {
    method: step.request.method,
    path: step.request.path,
    body:
      step.request.body === undefined
        ? null
        : JSON.stringify(substitute(step.request.body)),
  };
}

export function workflowDifferences(step, response, captures) {
  const differences =
    response.status === step.want.status
      ? []
      : [`HTTP status: got ${response.status}, expected ${step.want.status}`];
  if (step.want.body)
    differences.push(...expectationDifferences(response.data, step.want.body));
  if (step.want.same_as) {
    const snapshot = captures[step.want.same_as];
    if (!snapshot)
      differences.push(`Missing saved snapshot: ${step.want.same_as}`);
    else {
      differences.push(...expectationDifferences(response.data, snapshot));
      differences.push(...expectationDifferences(snapshot, response.data));
    }
  }
  return differences;
}

// Persist one step at a time. A crash repeats the same identities and body.
export class AssignmentRunner {
  constructor({
    plan,
    state,
    client,
    save,
    changed = () => {},
    sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
  }) {
    Object.assign(this, { plan, state, client, save, changed, sleep });
    this.running = false;
    this.stopRequested = false;
  }

  async step() {
    if (this.running) throw new Error("A scenario step is already running.");
    if (this.state.index >= this.plan.steps.length)
      return { done: true, differences: [] };
    this.running = true;
    this.stopRequested = false;
    try {
      const step = this.plan.steps[this.state.index];
      const request = workflowRequest(step, this.state.source);
      // Write the source and cursor before the first financial operation.
      this.save(this.state);
      let response = await this.client.send(request, step.name);
      if (step.fault?.lose_response && response.status === step.want.status) {
        response.record.simulatedLoss = true;
        // The exact original command is retried, including its operation key.
        response = await this.client.retry(response.record);
      }
      let differences = workflowDifferences(
        step,
        response,
        this.state.captures,
      );
      for (
        let attempt = 1;
        step.await &&
        differences.length &&
        attempt < ACCOUNTING_ATTEMPTS &&
        !this.stopRequested;
        attempt++
      ) {
        if (
          response.status !== step.want.status ||
          Number(response.data?.processing_errors) > 0
        )
          break;
        await this.sleep(ACCOUNTING_POLL_MS);
        if (this.stopRequested) break;
        response = await this.client.send(
          request,
          `${step.name} (accounting refresh)`,
        );
        differences = workflowDifferences(step, response, this.state.captures);
      }
      this.state.last = {
        name: step.name,
        status: response.status,
        differences,
        actual: response.data,
      };
      if (!differences.length) {
        if (step.capture) this.state.captures[step.capture] = response.data;
        this.state.completed.push({ name: step.name, status: response.status });
        this.state.index++;
      }
      this.save(this.state);
      this.changed();
      return { done: this.state.index >= this.plan.steps.length, differences };
    } finally {
      this.running = false;
    }
  }

  stop() {
    this.stopRequested = true;
  }
}
