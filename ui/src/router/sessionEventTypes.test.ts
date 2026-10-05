import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import {
  SESSION_SHELL_REFETCH_EVENT_TYPES,
  WORKSPACE_SESSION_EVENT_TYPES,
  WORKTREE_CARD_EVENT_TYPES,
} from "./sessionEventTypes";

// One type the Router doesn't know fails the whole shared SSE stream, not
// just its subscriber (events.ts probeRejectedTypes) — so every type these
// lists subscribe to must be a declared contracts.EventType.
describe("session event types", () => {
  it("are all declared in pkg/contracts/event.go", () => {
    const go = readFileSync("../pkg/contracts/event.go", "utf8");
    const declared = new Set([...go.matchAll(/EventType = "([a-z_.]+)"/g)].map((m) => m[1]));
    for (const type of [
      ...SESSION_SHELL_REFETCH_EVENT_TYPES,
      ...WORKSPACE_SESSION_EVENT_TYPES,
      ...WORKTREE_CARD_EVENT_TYPES,
    ]) {
      expect(declared, type).toContain(type);
    }
  });
});
