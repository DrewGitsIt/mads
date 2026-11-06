import { describe, expect, it } from "vitest";
import { fixtureIsolatedSession, fixtureSessions } from "../../router/fixtures";
import type { Session } from "../../router/types";
import { unpushedRepos, workState } from "./workState";

const inPlace: Session = { ...fixtureSessions["session-active"], uncommitted_count: 0 };
const landed: Session = { ...fixtureIsolatedSession, uncommitted_count: 0, unlanded_count: 0 };

describe("workState — Push (COMMIT-LAND-PUSH D5)", () => {
  it("pushes the branch of a session working in place once everything is committed", () => {
    expect(workState({ ...inPlace, unpushed_count: 2 })).toEqual({ kind: "push", commits: 2, target: "branch", repos: 1 });
  });

  it("pushes an isolated session's base once it has landed", () => {
    expect(workState({ ...landed, unpushed_count: 3 })).toEqual({ kind: "push", commits: 3, target: "base", repos: 1 });
  });

  it("comes after Commit, Land and changes outside the session", () => {
    expect(workState({ ...inPlace, uncommitted_count: 1, unpushed_count: 2 })?.kind).toBe("commit");
    expect(workState({ ...landed, unlanded_count: 1, unpushed_count: 2 })?.kind).toBe("land");
    expect(workState({ ...landed, outside_count: 1, unpushed_count: 2 })?.kind).toBe("outside");
    expect(workState({ ...inPlace, unpushed_count: 0 })).toBeNull();
  });

  it("counts the repos with commits to push", () => {
    const session: Session = {
      ...inPlace,
      unpushed_count: 4,
      repos: [
        { root: "/r/mads", unpushed_count: 3 },
        { root: "/r/learn", unpushed_count: 1 },
        { root: "/r/other" },
      ],
    };
    expect(unpushedRepos(session)).toBe(2);
    expect(workState(session)).toEqual({ kind: "push", commits: 4, target: "branch", repos: 2 });
  });
});
