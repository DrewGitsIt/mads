import { describe, expect, it } from "vitest";
import { compactionSupport } from "./compaction";
import type { ProviderInfo } from "./types";

const infos: ProviderInfo[] = [
  { name: "claude-code", matrix: { compaction: "native", compaction_focus: "native" } },
  { name: "goose", matrix: { compaction: "native", compaction_focus: "unsupported" } },
  { name: "kiro-cli", matrix: { compaction: "unsupported", compaction_focus: "unsupported" } },
  { name: "old-router", matrix: { interactive: "native" } },
];

describe("compactionSupport", () => {
  it("reads both rows from the provider's matrix", () => {
    expect(compactionSupport(infos, "claude-code")).toEqual({ compacts: true, takesFocus: true });
    expect(compactionSupport(infos, "goose")).toEqual({ compacts: true, takesFocus: false });
    expect(compactionSupport(infos, "kiro-cli")).toEqual({ compacts: false, takesFocus: false });
  });

  it("treats an unknown provider or a matrix without the rows as supported", () => {
    expect(compactionSupport([], "claude-code")).toEqual({ compacts: true, takesFocus: true });
    expect(compactionSupport(infos, undefined)).toEqual({ compacts: true, takesFocus: true });
    expect(compactionSupport(infos, "old-router")).toEqual({ compacts: true, takesFocus: true });
  });
});
