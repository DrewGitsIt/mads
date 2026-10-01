import { describe, expect, it } from "vitest";
import { agentIndex, agentNameForRef, isAgentId } from "./agentRef";
import type { Agent } from "./types";

function makeAgent(overrides: Partial<Agent>): Agent {
  return {
    id: "agt_1",
    name: "agent",
    prompt: "",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

const PARTNER = makeAgent({ id: "agt_3212544bb2e37c92", name: "mads-coding-partner" });
const BUILTIN = makeAgent({ id: "agt_system_architect", name: "architect" });

describe("agentIndex", () => {
  it("answers for both forms a reference may take", () => {
    const index = agentIndex([PARTNER]);
    expect(index.get("agt_3212544bb2e37c92")).toBe(PARTNER);
    expect(index.get("mads-coding-partner")).toBe(PARTNER);
  });

  it("lets an id win over a name that collides with it", () => {
    const impostor = makeAgent({ id: "agt_x", name: "agt_system_architect" });
    expect(agentIndex([impostor, BUILTIN]).get("agt_system_architect")).toBe(BUILTIN);
  });
});

describe("isAgentId", () => {
  it("recognises both the minted and the built-in form", () => {
    expect(isAgentId("agt_3212544bb2e37c92")).toBe(true);
    expect(isAgentId("agt_system_architect")).toBe(true);
    expect(isAgentId("mads-coding-partner")).toBe(false);
  });
});

describe("agentNameForRef", () => {
  it("names the persona an id points at", () => {
    expect(agentNameForRef("agt_3212544bb2e37c92", agentIndex([PARTNER]))).toBe(
      "mads-coding-partner",
    );
  });

  it("passes a reference that is already a name straight through", () => {
    expect(agentNameForRef("hand-written", agentIndex([]))).toBe("hand-written");
  });

  // Deliberately unlike workspaceNameForRef, which falls back to the reference:
  // an unresolved `agt_…` is a hash and nothing else, and showing one is the
  // bug this module exists to prevent.
  it("gives nothing for an id it cannot resolve, rather than the hash", () => {
    expect(agentNameForRef("agt_deleted", agentIndex([]))).toBeUndefined();
  });

  it("gives nothing for no reference at all", () => {
    expect(agentNameForRef(undefined, agentIndex([PARTNER]))).toBeUndefined();
  });
});
