import { describe, expect, it } from "vitest";
import { behaviorLabel, capabilityTags, differingBehaviors, providerLabel, topCapabilityTags } from "./providerBehaviors";
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

describe("providerLabel", () => {
  it("names the Codex CLI so it reads as ChatGPT's too", () => {
    expect(providerLabel("codex")).toBe("Codex (ChatGPT)");
    expect(providerLabel("claude-code")).toBe("Claude Code");
  });

  it("keeps ids that already read as names", () => {
    expect(providerLabel("goose")).toBe("goose");
    expect(providerLabel("kiro-cli")).toBe("kiro-cli");
  });
});

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

describe("topCapabilityTags", () => {
  const tag = (behavior: string, tone: "bad" | "warn") => ({ behavior, tone, label: behavior });

  it("keeps three, No … ahead of … emulated, and counts the rest", () => {
    const tags = [tag("a", "warn"), tag("b", "bad"), tag("c", "warn"), tag("d", "bad")];
    expect(topCapabilityTags(tags)).toEqual({ shown: [tag("b", "bad"), tag("d", "bad"), tag("a", "warn")], hidden: 1 });
  });

  it("hides nothing when they all fit", () => {
    expect(topCapabilityTags([tag("a", "warn")])).toEqual({ shown: [tag("a", "warn")], hidden: 0 });
  });
});

describe("differingBehaviors", () => {
  it("drops rows where every provider is native", () => {
    const fake: ProviderInfo = { name: "fake", matrix: { ...goose.matrix, branching: "native" } };
    const rows = differingBehaviors([goose, fake], behaviors).map((b) => b.behavior);
    expect(rows).toEqual(["branching", "mcp_servers", "trust_read_only"]);
  });
});
