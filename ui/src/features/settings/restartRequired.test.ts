import { describe, expect, it } from "vitest";
import { RESTART_REQUIRED_FIELDS } from "./restartRequired";
import { restartFieldsIn } from "./dirtyFields";

describe("restartRequired", () => {
  it("flags the restart-required fields among changed ones", () => {
    expect(restartFieldsIn(["concurrency_limit", "default_provider", "synthesis_debounce_ms"])).toEqual([
      "concurrency_limit",
      "synthesis_debounce_ms",
    ]);
  });

  it("does not flag feature_flags — it applies live, unlike Go's canonical restart set", () => {
    expect(restartFieldsIn(["feature_flags"])).toEqual([]);
  });

  it("only classifies the documented fields as restart-required (mirrors pkg/contracts/config.go RestartRequiredFields)", () => {
    expect([...RESTART_REQUIRED_FIELDS].sort()).toEqual(
      ["concurrency_limit", "log_retention_days", "synthesis_debounce_ms"].sort(),
    );
  });
});
