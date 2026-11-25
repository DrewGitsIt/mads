import { describe, expect, it } from "vitest";
import { allOk, renderChatSummary, renderChecks, renderReport } from "./report";
import type { Plan, Result } from "./measure";

const plan = {
  builds: [{ label: "base" }, { label: "cand" }],
  checks: ["a", "c"],
} as unknown as Plan;

const result = (check: string, build: string, steps: { name: string; ok: boolean; known?: string }[]): Result =>
  ({
    scenario: `check:${check}`,
    build,
    ok: steps.every((s) => s.ok || s.known),
    checks: steps.map((s) => ({ name: s.name, ok: s.ok, detail: s.ok ? undefined : `${build} failed`, knownOnMain: s.known })),
  }) as unknown as Result;

describe("renderChecks", () => {
  it("resolves short check names and flags a candidate regression", () => {
    const md = renderChecks(plan, [
      result("a-permission", "base", [{ name: "after a reload", ok: true }]),
      result("a-permission", "cand", [{ name: "after a reload", ok: false }]),
      result("c-retro-progress", "base", [{ name: "after starting it", ok: true }]),
      result("c-retro-progress", "cand", [{ name: "after starting it", ok: true }]),
    ]).join("\n");
    expect(md).not.toContain("missing");
    expect(md).toContain("CANDIDATE REGRESSION");
    expect(md).toContain("a-permission: after a reload");
    expect(md).toContain("cand failed");
  });

  it("reports FIXED and known-fail, and a UI-only regression doesn't raise the banner", () => {
    const md = renderChecks(plan, [
      result("a-permission", "base", [{ name: "after Retro → Sessions", ok: false, known: "dedupe" }]),
      result("a-permission", "cand", [{ name: "after Retro → Sessions", ok: true }]),
      result("c-retro-progress", "base", [{ name: "after starting it", ok: true }]),
      result("c-retro-progress", "cand", [{ name: "after starting it", ok: false }]),
    ]).join("\n");
    expect(md).toContain("★ FIXED");
    expect(md).not.toContain("CANDIDATE REGRESSION");
    expect(md).toContain("UI-only");
  });

  it("a check that never ran reads as missing, not as passing", () => {
    const md = renderChecks(plan, [result("c-retro-progress", "base", [{ name: "x", ok: true }])]).join("\n");
    expect(md).toContain("missing");
  });
});

describe("renderReport (realism)", () => {
  it("a scenario the real database can't drive reads as not run, and doesn't fail the run", () => {
    const p = { runDir: "/x/r", builds: [{ label: "base" }], scenarios: ["3t-open-delegate-typical"], modes: ["off"], n: 1, checks: [], meta: {}, realism: true } as unknown as Plan;
    const rs = [{ scenario: "3t-open-delegate-typical", build: "base", mode: "off", rep: 1, ok: true, skipped: true, legs: [], checks: [] }] as unknown as Result[];
    const md = renderReport(p, rs);
    expect(md).toContain("not run (realism: no suitable data)");
    expect(md).not.toContain("FAILED");
    expect(allOk(p, rs)).toBe(true);
  });
});

describe("renderChatSummary", () => {
  const p = { runDir: "/x/r", builds: [{ label: "base" }], scenarios: ["10a-chat-page1", "10c-chat-all", "10ap-chat-page1-paced", "10cs-chat-all-slow"], n: 1, checks: [], meta: {} } as unknown as Plan;
  const res = (scenario: string, timing: Record<string, number>, ok = true): Result =>
    ({ scenario, build: "base", mode: "off", rep: 1, ok, det: { chat_rows_loaded: 100, chat_dom_nodes_per_row: 6.1 }, timing, checks: [{ name: "x", ok }] }) as unknown as Result;

  it("costs per delta unpaced, per second paced; a saturated cell flagged with its headline in bold", () => {
    const md = renderChatSummary(p, [
      res("10a-chat-page1", { chat_busy_pct: 22, chat_ms_per_delta: 3.71, chat_js_ms_per_delta: 2.456, chat_frame_p95_ms: 17.7, chat_probe_ms_per_s: 4.2 }),
      res("10c-chat-all", { chat_busy_pct: 99.9, chat_ms_per_delta: 16.6, chat_frame_p95_ms: 50.2, chat_commits_per_s: 54.7 }),
      res("10ap-chat-page1-paced", { chat_busy_pct: 18, chat_ms_per_s: 181, chat_js_ms_per_s: 99.5, chat_window_ms: 21000 }),
      res("10cs-chat-all-slow", { chat_busy_pct: 60, chat_ms_per_delta: 40.12 }),
    ]).join("\n");
    expect(md).toContain("### Unpaced");
    expect(md).toContain("### Paced");
    // 2 decimals per delta.
    expect(md).toMatch(/\| 10a-chat-page1 \| base \| 1\/1 \|  \| 100 \| 6\.1 \| – \| 22 \| 3\.71 \| 2\.46 \|/);
    expect(md).toMatch(/\| 10c-chat-all \| base \| 1\/1 \| ⚠ \|.*\*\*54\.7\*\*.*\*\*50\.2\*\*/);
    expect(md).not.toMatch(/\| 10cs-chat-all-slow \| base \| 1\/1 \| ⚠/);
    expect(md).toMatch(/\| 10ap-chat-page1-paced \| base \| 1\/1 \|  \| 100 \| 21 \| 18 \| 181 \| 99\.5 \|/);
    expect(md).toContain("Recorder overhead (base): 10a-chat-page1 4.2 ms/s");
    expect(md).toContain("4 / 4 repetitions passed");
  });

  it("lists a failed repetition's failing checks", () => {
    const md = renderChatSummary(p, [{ ...res("10a-chat-page1", {}, false), checks: [{ name: "the streamed reply rendered whole", ok: false, detail: "first difference at 12" }] } as unknown as Result]).join("\n");
    expect(md).toContain("✗ 10a-chat-page1 base rep 1: the streamed reply rendered whole: first difference at 12");
  });
});
