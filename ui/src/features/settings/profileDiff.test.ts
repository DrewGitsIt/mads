import { describe, expect, it } from "vitest";
import {
  computeCredentialDiff,
  computeProfileDiff,
  deepEqual,
  groupBySection,
  hasRestartChange,
  isCredentialActive,
  orderedDiffSections,
} from "./profileDiff";
import { FIELD_LABELS, FIELD_SECTION, SECTION_IDS } from "./sections";
import { fixtureConfig, fixtureProfile } from "../../router/fixtures";

describe("deepEqual", () => {
  it("compares objects by value, not reference", () => {
    expect(deepEqual({ a: 1, b: 2 }, { b: 2, a: 1 })).toBe(true);
    expect(deepEqual({ a: 1 }, { a: 2 })).toBe(false);
  });
});

describe("computeProfileDiff", () => {
  it("lists only the fields the target snapshot actually changes", () => {
    const entries = computeProfileDiff(fixtureConfig, fixtureProfile.snapshot);
    const fields = entries.map((e) => e.field).sort();
    expect(fields).toEqual(["concurrency_limit", "default_model_tier"]);
  });

  it("marks concurrency_limit as restart-required and default_model_tier as not", () => {
    const entries = computeProfileDiff(fixtureConfig, fixtureProfile.snapshot);
    const byField = Object.fromEntries(entries.map((e) => [e.field, e]));
    expect(byField.concurrency_limit.restart).toBe(true);
    expect(byField.default_model_tier.restart).toBe(false);
    expect(byField.concurrency_limit.oldValue).toBe(3);
    expect(byField.concurrency_limit.newValue).toBe(1);
  });

  it("returns nothing when the snapshot matches current config", () => {
    const entries = computeProfileDiff(fixtureConfig, { synthesis_mode: fixtureConfig.synthesis_mode });
    expect(entries).toEqual([]);
  });

  it("ignores the Router's response-only host fields an older snapshot captured", () => {
    const entries = computeProfileDiff(fixtureConfig, {
      platform: "darwin",
      fallback_dir: "/Users/someone/elsewhere",
    });
    expect(entries).toEqual([]);
  });
});

describe("groupBySection / orderedDiffSections", () => {
  it("groups diff entries by the section their field's input lives in", () => {
    const entries = computeProfileDiff(fixtureConfig, fixtureProfile.snapshot);
    const grouped = groupBySection(entries);
    expect(grouped.pipelines?.map((e) => e.field)).toEqual(["concurrency_limit"]);
    expect(grouped.synthesis?.map((e) => e.field)).toEqual(["default_model_tier"]);
    expect(orderedDiffSections(grouped)).toEqual(["pipelines", "synthesis"]);
  });
});

describe("sections that own no Config field", () => {
  it("never collect a diff entry", () => {
    for (const id of ["profiles", "workspaces", "about"] as const) {
      expect(SECTION_IDS).toContain(id);
      expect(Object.values(FIELD_SECTION)).not.toContain(id);
    }
    expect(orderedDiffSections({})).toEqual([]);
  });

  it("puts the newly mapped fields where their inputs now live", () => {
    const entries = computeProfileDiff(fixtureConfig, {
      display_mode: "compact",
      log_retention_days: 3,
      synthesis_auto_approve: true,
      pipeline_stage_timeout_minutes: 99,
    });
    const grouped = groupBySection(entries);
    expect(grouped.appearance?.map((e) => e.field)).toEqual(["display_mode"]);
    expect(grouped.system?.map((e) => e.field)).toEqual(["log_retention_days"]);
    expect(grouped.synthesis?.map((e) => e.field)).toEqual(["synthesis_auto_approve"]);
    expect(grouped.pipelines?.map((e) => e.field)).toEqual(["pipeline_stage_timeout_minutes"]);
  });

  it("every labelled field still maps to a section", () => {
    for (const field of Object.keys(FIELD_LABELS)) {
      expect(FIELD_SECTION[field as keyof typeof FIELD_SECTION]).toBeDefined();
    }
  });
});

describe("hasRestartChange", () => {
  it("is true when any entry is restart-required", () => {
    const entries = computeProfileDiff(fixtureConfig, fixtureProfile.snapshot);
    expect(hasRestartChange(entries)).toBe(true);
  });

  it("is false when no entry is restart-required", () => {
    const entries = computeProfileDiff(fixtureConfig, { default_model_tier: "cheapest" });
    expect(hasRestartChange(entries)).toBe(false);
  });
});

describe("isCredentialActive", () => {
  it("treats empty/absent bindings as every credential active", () => {
    expect(isCredentialActive("anthropic", undefined)).toBe(true);
    expect(isCredentialActive("anthropic", [])).toBe(true);
  });

  it("checks membership when bindings are present", () => {
    expect(isCredentialActive("anthropic", ["anthropic"])).toBe(true);
    expect(isCredentialActive("openai", ["anthropic"])).toBe(false);
  });
});

describe("computeCredentialDiff", () => {
  it("lists only credentials whose active state changes", () => {
    const entries = computeCredentialDiff(
      ["anthropic", "openai", "azure"],
      undefined,
      ["anthropic"],
    );
    expect(entries).toEqual([
      { name: "openai", wasActive: true, willBeActive: false },
      { name: "azure", wasActive: true, willBeActive: false },
    ]);
  });

  it("returns nothing when bindings are equivalent", () => {
    const entries = computeCredentialDiff(["anthropic"], undefined, undefined);
    expect(entries).toEqual([]);
  });
});
