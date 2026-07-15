import { describe, expect, it } from "vitest";
import { dirtyRestartFields, RESTART_REQUIRED_FIELDS } from "./restartRequired";
import { fixtureConfig } from "../../router/fixtures";

describe("restartRequired", () => {
  it("flags a changed restart-required field", () => {
    const dirty = dirtyRestartFields(fixtureConfig, { ...fixtureConfig, concurrency_limit: 9 });
    expect(dirty).toEqual(["concurrency_limit"]);
  });

  it("does not flag a live (non-restart-required) field", () => {
    const dirty = dirtyRestartFields(fixtureConfig, { ...fixtureConfig, default_provider: "fake" });
    expect(dirty).toEqual([]);
  });

  it("flags feature_flags by deep value, not reference", () => {
    const dirty = dirtyRestartFields(fixtureConfig, {
      ...fixtureConfig,
      feature_flags: { ...fixtureConfig.feature_flags, multi_repo: true },
    });
    expect(dirty).toEqual(["feature_flags"]);
  });

  it("only classifies the documented fields as restart-required", () => {
    expect([...RESTART_REQUIRED_FIELDS].sort()).toEqual(
      ["concurrency_limit", "feature_flags", "log_retention_days"].sort(),
    );
  });
});
