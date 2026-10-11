// Portal metadata describes startup configuration; business decisions stay in Go.
export async function loadRuntime(fetcher = fetch) {
  const response = await fetcher("/demo/runtime.json", { cache: "no-store" });
  if (!response.ok)
    throw new Error(`Clock metadata could not be loaded: HTTP ${response.status}`);
  const runtime = await response.json();
  if (runtime.clock_mode === "real" && runtime.fixed_now === undefined)
    return runtime;
  if (
    runtime.clock_mode === "fixed" &&
    typeof runtime.fixed_now === "string" &&
    Number.isFinite(new Date(runtime.fixed_now).getTime())
  )
    return runtime;
  throw new Error("Invalid demo clock metadata.");
}

export function businessTime(runtime, realNow = new Date()) {
  return runtime?.clock_mode === "fixed"
    ? new Date(runtime.fixed_now)
    : realNow;
}

// Timeline fixtures require the same explicit clock in API, worker and portal.
export function requireBusinessTime(step, runtime) {
  if (!step.business_time) return;
  if (runtime?.clock_mode !== "fixed" || new Date(runtime.fixed_now).getTime() !== new Date(step.business_time).getTime())
    throw new Error(`This step requires E2B_DEMO_NOW=${step.business_time}. Start services with that clock while preserving volumes, then reload this page.`);
}
