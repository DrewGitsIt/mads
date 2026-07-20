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

  it("flags a changed synthesis_debounce_ms", () => {
    const dirty = dirtyRestartFields(fixtureConfig, {
      ...fixtureConfig,
      synthesis_debounce_ms: fixtureConfig.synthesis_debounce_ms + 500,
    });
    expect(dirty).toEqual(["synthesis_debounce_ms"]);
  });

  it("does not flag feature_flags — it applies live, unlike Go's canonical restart set", () => {
    const dirty = dirtyRestartFields(fixtureConfig, {
      ...fixtureConfig,
      feature_flags: { ...fixtureConfig.feature_flags, multi_repo: true },
    });
    expect(dirty).toEqual([]);
  });

  it("only classifies the documented fields as restart-required (mirrors pkg/contracts/config.go RestartRequiredFields)", () => {
    expect([...RESTART_REQUIRED_FIELDS].sort()).toEqual(
      ["concurrency_limit", "log_retention_days", "synthesis_debounce_ms"].sort(),
    );
  });
});
