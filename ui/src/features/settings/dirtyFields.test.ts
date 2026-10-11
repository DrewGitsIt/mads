import { describe, expect, it } from "vitest";
import { dirtyFields, restartFieldsIn } from "./dirtyFields";
import { fixtureConfig } from "../../router/fixtures";

describe("dirtyFields", () => {
  it("is empty when draft matches saved", () => {
    expect(dirtyFields(fixtureConfig, { ...fixtureConfig })).toEqual([]);
  });

  it("lists each top-level key that changed", () => {
    const draft = { ...fixtureConfig, concurrency_limit: 9, display_mode: "compact" };
    expect(dirtyFields(fixtureConfig, draft).sort()).toEqual(["concurrency_limit", "display_mode"]);
  });

  it("counts a compound field once, however many entries changed", () => {
    const draft = {
      ...fixtureConfig,
      credential_bindings: ["a", "b", "c"],
      env_passthrough: ["PATH", "HOME"],
    };
    expect(dirtyFields(fixtureConfig, draft).sort()).toEqual(["credential_bindings", "env_passthrough"]);
  });

  it("ignores response-only keys", () => {
    const draft = { ...fixtureConfig, platform: "plan9", fallback_dir: "/elsewhere" };
    expect(dirtyFields(fixtureConfig, draft)).toEqual([]);
  });

  it("treats an omitted optional field and its cleared value as equal", () => {
    const saved = { ...fixtureConfig, editor_command: undefined, env_passthrough: undefined };
    const draft = { ...fixtureConfig, editor_command: "", env_passthrough: [] };
    expect(dirtyFields(saved, draft)).toEqual([]);
  });
});

describe("restartFieldsIn", () => {
  it("keeps only restart-required fields", () => {
    expect(restartFieldsIn(["concurrency_limit", "theme", "log_retention_days"])).toEqual([
      "concurrency_limit",
      "log_retention_days",
    ]);
  });
});
