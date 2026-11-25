import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { test, expect } from "@playwright/test";
import { readFileSync } from "node:fs";
import { abortActive, loadPlan, runOne, scrubError, type Result } from "./measure";
import { checkByName, runCheck } from "./checks";
import type { Manifest } from "./scenarios";
import { scenarioById } from "./scenarios";
import { strandedRepetitions } from "./stack";

/**
 * One test per (scenario, delay mode, repetition, build). Within a scenario
 * and mode the builds alternate ABBA (A B, B A, A B, ...) so drift lands on
 * both alike. Each test boots its own stack on a fresh copy of the run's one
 * seeded database, in its own browser (or Electron app), and writes its raw
 * result to <runDir>/raw/. A repetition that fails (ok=false) fails its
 * test and the run goes on; a worker that dies leaves its pid registry
 * file, and every test after it fails at once (see below). globalTeardown
 * writes the report, failures and missing repetitions included.
 */
const plan = loadPlan();

// A repetition that times out never reaches runOne's own teardown.
test.afterEach(async () => {
  await abortActive();
});

function save(r: Result): void {
  const dir = path.join(plan!.runDir, "raw", r.scenario.replace(":", "-"), r.mode);
  mkdirSync(dir, { recursive: true });
  writeFileSync(path.join(dir, `${r.build}-${r.rep}.json`), JSON.stringify(r, null, 2));
}

if (!plan) {
  test("perf harness needs a plan", () => {
    test.skip(true, "run through `make ui-perf` (PERF_PLAN unset)");
  });
} else {
  for (const id of plan.scenarios) {
    const scenario = scenarioById(id);
    for (const mode of plan.modes) {
      // Real git (9x): the delay mock would only add to it; chat (10x): the
      // delays are the git routes', not what it measures. Delays off only.
      if ((scenario.git || scenario.delaysOffOnly) && mode === "on") continue;
      for (let rep = 1; rep <= plan.n; rep++) {
        const order = rep % 2 === 1 ? plan.builds : [...plan.builds].reverse();
        for (const build of order) {
          test(`${scenario.id} · delays ${mode} · ${build.label} · rep ${rep}`, async () => {
            // A repetition that ends cleanly deletes its pid registry file; one
            // left behind means a worker died mid-repetition. Its processes may
            // still be running, so nothing more is measured: every later test
            // fails here at once and run.sh reaps them.
            const stranded = strandedRepetitions();
            expect(stranded, "a worker died mid-repetition; the run is aborted").toEqual([]);
            let result: Result;
            try {
              result = await runOne({ plan, build, scenario, rep, mode });
            } catch (err) {
              const error = scrubError(plan, undefined, `FATAL: ${err instanceof Error ? err.message : String(err)}`);
              save({ scenario: scenario.id, build: build.label, mode, rep, browser: plan.browser, ok: false, error } as Result);
              throw err;
            }
            // Realism: a real database may simply lack what a scenario needs
            // (no delegate without a transcript file, no typical parent). That
            // is "not run", not a failure.
            if (plan.realism && !result.ok && /the workload has no such/.test(result.error ?? "")) {
              save({ ...result, ok: true, skipped: true, legs: [] });
              test.skip(true, `realism: ${result.error}`);
              return;
            }
            save(result);
            const legs = result.legs.map((l) => `${l.name} ${l.settledMs}ms`).join(", ");
            console.log(
              `${scenario.id} ${mode} ${build.label}#${rep}: ` +
                (result.ok
                  ? `events ${(result.det.events_stream_bytes_proxy / 1e6).toFixed(1)} MB, all-6-busy ${result.timing.all_sockets_busy_ms}ms, ` +
                    `queued total ${result.timing.queued_total_ms}ms (max ${result.timing.queued_max_ms}); ${legs}`
                  : `FAILED ${result.error}`),
            );
            // A failed repetition fails its test (T6); the run goes on to the next.
            expect(result.ok, result.error).toBe(true);
          });
        }
      }
    }
  }
  // Correctness checks: once per build, a fresh stack each.
  for (const name of plan.checks) {
    const check = checkByName(name);
    for (const build of plan.builds) {
      test(`check ${check.name} · ${build.label}`, async () => {
        expect(strandedRepetitions(), "a worker died mid-repetition; the run is aborted").toEqual([]);
        const manifest = JSON.parse(readFileSync(plan.manifestPath, "utf8")) as Manifest;
        const result = await runCheck({ plan, build, name: check.name, manifest });
        save(result);
        console.log(`check ${check.name} ${build.label}: ${result.ok ? "✓" : "✗"} ${result.checks.map((c) => `${c.ok ? "✓" : "✗"} ${c.name}${c.detail ? ` (${c.detail})` : ""}`).join("; ")}${result.error && !result.checks.length ? ` ${result.error}` : ""}`);
        expect(result.ok, result.error).toBe(true);
      });
    }
  }
}
