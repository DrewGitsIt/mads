import { describe, expect, it } from "vitest";
import { behaviorLabel, capabilityTags, differingBehaviors } from "./providerBehaviors";
import type { BehaviorInfo, ProviderInfo } from "../../router/types";

const behaviors: BehaviorInfo[] = [
  { behavior: "interrupt", level: "required" },
  { behavior: "branching", level: "optional" },
  { behavior: "mcp_servers", level: "optional" },
  { behavior: "trust_read_only", level: "expected" },
  { behavior: "resume", level: "expected" },
];

const goose: ProviderInfo = {
  name: "goose",
  matrix: {
    interrupt: "native",
    branching: "unsupported",
    mcp_servers: "unsupported",
    trust_read_only: "emulated",
    resume: "native",
  },
};

describe("behaviorLabel", () => {
  it("reads known keys as product names", () => {
    expect(behaviorLabel("delegate_lifecycle")).toBe("Subagent cards");
    expect(behaviorLabel("cwd_reroot")).toBe("Folder reroot");
    expect(behaviorLabel("one_shot")).toBe("One-shot");
  });

  it("humanizes a key it doesn't know", () => {
    expect(behaviorLabel("future_thing")).toBe("Future thing");
  });
});

describe("capabilityTags", () => {
  it("lists only what isn't native: unsupported as No …, emulated as … emulated", () => {
    expect(capabilityTags(goose, behaviors)).toEqual([
      { behavior: "branching", tone: "bad", label: "No branching" },
      { behavior: "mcp_servers", tone: "bad", label: "No MCP servers" },
      { behavior: "trust_read_only", tone: "warn", label: "Read-only trust emulated" },
    ]);
  });

  it("says nothing for an all-native provider, or behaviours it declares nothing for", () => {
    expect(capabilityTags({ name: "x", matrix: { interrupt: "native" } }, behaviors)).toEqual([]);
  });
});

describe("differingBehaviors", () => {
  it("drops rows where every provider is native", () => {
    const fake: ProviderInfo = { name: "fake", matrix: { ...goose.matrix, branching: "native" } };
    const rows = differingBehaviors([goose, fake], behaviors).map((b) => b.behavior);
    expect(rows).toEqual(["branching", "mcp_servers", "trust_read_only"]);
  });
});
