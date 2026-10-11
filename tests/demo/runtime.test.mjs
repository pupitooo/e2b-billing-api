import test from "node:test";
import assert from "node:assert/strict";
import { businessTime, loadRuntime, requireBusinessTime } from "../../demo/runtime.mjs";
import { invoiceEligible } from "../../demo/core.mjs";

// loadRuntime reads portal configuration, rejects failed or malformed metadata,
// and preserves offset and precision text independently of financial parsing.
test("loadRuntime", async (t) => {
  const cases = [
    { scenario: "real clock", inputStatus: 200, inputBody: { clock_mode: "real" }, wantRuntime: { clock_mode: "real" }, wantError: null },
    { scenario: "fixed clock with microseconds and an offset", inputStatus: 200, inputBody: { clock_mode: "fixed", fixed_now: "2026-11-01T08:00:00.123456+08:00" }, wantRuntime: { clock_mode: "fixed", fixed_now: "2026-11-01T08:00:00.123456+08:00" }, wantError: null },
    { scenario: "missing metadata", inputStatus: 404, inputBody: {}, wantRuntime: null, wantError: "Clock metadata could not be loaded: HTTP 404" },
    { scenario: "missing fixed instant", inputStatus: 200, inputBody: { clock_mode: "fixed" }, wantRuntime: null, wantError: "Invalid demo clock metadata." },
    { scenario: "invalid fixed instant", inputStatus: 200, inputBody: { clock_mode: "fixed", fixed_now: "bad" }, wantRuntime: null, wantError: "Invalid demo clock metadata." },
    { scenario: "unknown mode", inputStatus: 200, inputBody: { clock_mode: "other" }, wantRuntime: null, wantError: "Invalid demo clock metadata." },
  ];
  for (const c of cases) {
    await t.test(c.scenario, async () => {
      const requests = [];
      let got = null;
      let error = null;
      try {
        got = await loadRuntime(async (path, options) => {
          requests.push({ path, options });
          return new Response(JSON.stringify(c.inputBody), { status: c.inputStatus });
        });
      } catch (err) { error = err.message; }

      assert.equal(error, c.wantError, `loadRuntime(${JSON.stringify(c.inputBody)}): error`);
      assert.deepEqual(got, c.wantRuntime, `loadRuntime(${JSON.stringify(c.inputBody)}): metadata`);
      assert.deepEqual(requests, [{ path: "/demo/runtime.json", options: { cache: "no-store" } }], "metadata uses the portal and bypasses cache");
    });
  }
});

// businessTime keeps ordinary browser time live, holds fixed time, and checks
// the completed-month boundary using the displayed business clock.
test("businessTime", async (t) => {
  const cases = [
    { scenario: "real mode uses each supplied current instant", inputRuntime: { clock_mode: "real" }, inputRealTime: "2026-10-31T23:59:59.999Z", inputMonth: "2026-10", wantTime: "2026-10-31T23:59:59.999Z", wantEligible: false },
    { scenario: "fixed mode ignores the earlier browser clock", inputRuntime: { clock_mode: "fixed", fixed_now: "2026-11-01T00:00:00Z" }, inputRealTime: "2026-10-11T12:00:00Z", inputMonth: "2026-10", wantTime: "2026-11-01T00:00:00.000Z", wantEligible: true },
    { scenario: "fixed instant before boundary remains ineligible", inputRuntime: { clock_mode: "fixed", fixed_now: "2026-10-31T23:59:59.999999Z" }, inputRealTime: "2026-12-01T00:00:00Z", inputMonth: "2026-10", wantTime: "2026-10-31T23:59:59.999Z", wantEligible: false },
    { scenario: "missing metadata uses advisory browser time", inputRuntime: null, inputRealTime: "2026-11-01T00:00:00Z", inputMonth: "2026-10", wantTime: "2026-11-01T00:00:00.000Z", wantEligible: true },
  ];
  for (const c of cases) {
    await t.test(c.scenario, () => {
      const got = businessTime(c.inputRuntime, new Date(c.inputRealTime));

      assert.equal(got.toISOString(), c.wantTime, `businessTime(${JSON.stringify(c.inputRuntime)}): instant`);
      assert.equal(invoiceEligible(c.inputMonth, got), c.wantEligible, `invoiceEligible(${c.inputMonth}, ${got.toISOString()})`);
    });
  }
});

// requireBusinessTime accepts the fixture's trusted instant and rejects mismatched
// or real clocks before a timeline step can send a financial request.
test("requireBusinessTime", async (t) => {
 const cases = [
  {scenario:"matching fixed instant", inputStep:{business_time:"2026-11-01T00:00:00Z"}, inputRuntime:{clock_mode:"fixed",fixed_now:"2026-11-01T00:00:00Z"},wantError:false},
  {scenario:"equivalent UTC offset",inputStep:{business_time:"2026-11-01T00:00:00Z"},inputRuntime:{clock_mode:"fixed",fixed_now:"2026-11-01T08:00:00+08:00"},wantError:false},
  {scenario:"another fixed month",inputStep:{business_time:"2026-11-01T00:00:00Z"},inputRuntime:{clock_mode:"fixed",fixed_now:"2026-12-01T00:00:00Z"},wantError:true},
  {scenario:"real clock cannot reproduce a timeline receipt",inputStep:{business_time:"2026-11-01T00:00:00Z"},inputRuntime:{clock_mode:"real"},wantError:true},
  {scenario:"ordinary workflow has no clock requirement",inputStep:{},inputRuntime:{clock_mode:"real"},wantError:false},
 ];
 for (const c of cases) await t.test(c.scenario,()=>{
  let error=null;
  try{requireBusinessTime(c.inputStep,c.inputRuntime);}catch(err){error=err;}
  assert.equal(Boolean(error),c.wantError,`step=${JSON.stringify(c.inputStep)} runtime=${JSON.stringify(c.inputRuntime)} error=${error}`);
 });
});
