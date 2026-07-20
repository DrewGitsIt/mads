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
});

describe("groupBySection / orderedDiffSections", () => {
  it("groups diff entries by the section their field's input lives in", () => {
    const entries = computeProfileDiff(fixtureConfig, fixtureProfile.snapshot);
    const grouped = groupBySection(entries);
    expect(grouped.general?.map((e) => e.field)).toEqual(["concurrency_limit"]);
    expect(grouped.pipeline?.map((e) => e.field)).toEqual(["default_model_tier"]);
    expect(orderedDiffSections(grouped)).toEqual(["general", "pipeline"]);
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
