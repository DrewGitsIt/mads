import { describe, expect, it } from "vitest";
import { classifySession, filterByKind } from "./sessionKind";
import type { Session } from "../../../router/types";

function makeSession(overrides: Partial<Session>): Session {
  return {
    id: "s1",
    name: "Session",
    activity: "inactive",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

describe("classifySession", () => {
  it("classifies a retro-tagged session as retro-generated", () => {
    expect(classifySession(makeSession({ tags: ["retro"] }))).toBe("retro-generated");
  });

  it("classifies an untagged session as work", () => {
    expect(classifySession(makeSession({}))).toBe("work");
  });
});

describe("filterByKind", () => {
  const sessions = [
    makeSession({ id: "s1", tags: ["retro"] }),
    makeSession({ id: "s2" }),
  ];

  it("returns everything for 'all'", () => {
    expect(filterByKind(sessions, "all")).toEqual(sessions);
  });

  it("filters to only work sessions", () => {
    expect(filterByKind(sessions, "work").map((s) => s.id)).toEqual(["s2"]);
  });

  it("filters to only retro-generated sessions", () => {
    expect(filterByKind(sessions, "retro-generated").map((s) => s.id)).toEqual(["s1"]);
  });
});
