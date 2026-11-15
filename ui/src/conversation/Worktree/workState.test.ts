import { describe, expect, it } from "vitest";
import { fixtureIsolatedRepoState, fixtureIsolatedSession, fixtureRepoWorktree, fixtureSessions } from "../../router/fixtures";
import type { Session, SessionRepoState } from "../../router/types";
import { pushFacts, workState } from "./workState";

const inPlace: Session = { ...fixtureSessions["session-active"], uncommitted_count: 0 };
const landed: Session = { ...fixtureIsolatedSession, uncommitted_count: 0, unlanded_count: 0 };

function inPlaceRepo(root: string, ahead: number, primary = false): SessionRepoState {
  return { root, primary: primary || undefined, worktree: fixtureRepoWorktree({ path: root, main: true, ahead }) };
}

function isolatedRepo(branchAhead: number, baseAhead: number): SessionRepoState {
  const r = structuredClone(fixtureIsolatedRepoState);
  r.worktree.ahead = branchAhead;
  r.base_worktree!.ahead = baseAhead;
  return r;
}

describe("pushFacts — what Push would send, from repo state (COMMIT-LAND-PUSH D10)", () => {
  it("is unknown until the repo state has answered", () => {
    expect(pushFacts(inPlace, null)).toBeUndefined();
    expect(workState(inPlace, undefined)).toBeNull();
  });

  it("in place: sums each checked-out branch's ahead", () => {
    const facts = pushFacts(inPlace, [inPlaceRepo("/r/mads", 3, true), inPlaceRepo("/r/learn", 1), inPlaceRepo("/r/quiet", 0)]);
    expect(facts).toEqual({
      commits: 4,
      repos: [
        { root: "/r/mads", primary: true, target: "branch", commits: 3 },
        { root: "/r/learn", primary: undefined, target: "branch", commits: 1 },
      ],
      branchAhead: undefined,
    });
  });

  it("isolated, after Land: the base in the user's checkout", () => {
    const facts = pushFacts(landed, [isolatedRepo(5, 2)]);
    expect(facts?.commits).toBe(2);
    expect(facts?.repos[0]).toMatchObject({ target: "base", commits: 2, isolated: true });
  });

  it("isolated, before Land: nothing to push next; the session branch is only the push for a PR", () => {
    const facts = pushFacts({ ...landed, unlanded_count: 3 }, [isolatedRepo(4, 2)]);
    expect(facts).toEqual({ commits: 0, repos: [], branchAhead: 4 });
  });
});

describe("workState — Push", () => {
  const facts = (s: Session, repos: SessionRepoState[]) => pushFacts(s, repos);

  it("pushes the branch of a session working in place once everything is committed", () => {
    expect(workState(inPlace, facts(inPlace, [inPlaceRepo("/r/mads", 2, true)]))).toEqual({
      kind: "push",
      commits: 2,
      target: "branch",
      repos: 1,
    });
  });

  it("pushes an isolated session's base once it has landed", () => {
    expect(workState(landed, facts(landed, [isolatedRepo(0, 3)]))).toEqual({ kind: "push", commits: 3, target: "base", repos: 1 });
  });

  it("offers Push beside Commit when both apply (D11), never before Land", () => {
    const dirty = { ...inPlace, uncommitted_count: 2 };
    expect(workState(dirty, facts(dirty, [inPlaceRepo("/r/mads", 3, true)]))).toEqual({
      kind: "commit",
      files: 2,
      thenLand: 0,
      repos: 1,
      push: { commits: 3, target: "branch", repos: 1 },
    });
    const unlanded = { ...landed, uncommitted_count: 1, unlanded_count: 2 };
    const state = workState(unlanded, facts(unlanded, [isolatedRepo(2, 3)]));
    expect(state).toMatchObject({ kind: "commit" });
    expect(state?.kind === "commit" && state.push).toBeFalsy();
  });

  it("comes after Land and changes outside the session", () => {
    expect(workState({ ...landed, unlanded_count: 1 }, facts(landed, [isolatedRepo(0, 2)]))?.kind).toBe("land");
    expect(workState({ ...landed, outside_count: 1 }, facts(landed, [isolatedRepo(0, 2)]))?.kind).toBe("outside");
    expect(workState(inPlace, facts(inPlace, [inPlaceRepo("/r/mads", 0, true)]))).toBeNull();
  });

  it("counts the repos with commits to push", () => {
    const state = workState(inPlace, facts(inPlace, [inPlaceRepo("/r/mads", 3, true), inPlaceRepo("/r/learn", 1)]));
    expect(state).toEqual({ kind: "push", commits: 4, target: "branch", repos: 2 });
  });

  it("never offers Push without repo state (the rail)", () => {
    expect(workState({ ...inPlace, uncommitted_count: 1 })).toEqual({ kind: "commit", files: 1, thenLand: 0, repos: 1 });
  });
});
