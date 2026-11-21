import { describe, expect, it } from "vitest";
import { allOk, renderChecks, renderReport } from "./report";
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
