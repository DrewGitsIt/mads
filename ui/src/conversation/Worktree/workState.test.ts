import { describe, expect, it } from "vitest";
import { fixtureIsolatedRepoState, fixtureIsolatedSession, fixtureRepoWorktree, fixtureSessions } from "../../router/fixtures";
import type { Session, SessionRepoState } from "../../router/types";
import { landRepos, pushFacts, workState } from "./workState";

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
  const facts = (s: Session, repos: SessionRepoState[]) => ({ push: pushFacts(s, repos) });

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

describe("landRepos and workState — Land for linked worktrees (D12)", () => {
  function linked(root: string, branch: string, toLand: number, base = "main"): SessionRepoState {
    return {
      root,
      worktree: fixtureRepoWorktree({ path: root, git_dir: "/r/mads/.git", branch, base, base_source: "recorded", to_land: toLand }),
    };
  }
  const main: SessionRepoState = { root: "/r/mads", primary: true, worktree: fixtureRepoWorktree({ path: "/r/mads", main: true, to_land: 9 }) };

  it("lists a linked worktree with work to land; never a main checkout, a detached one, or one with nothing", () => {
    const detached = linked("/r/detached", "x", 2);
    detached.worktree.detached = true;
    const repos = [main, linked("/r/mads-nested", "nested-delegates", 1), linked("/r/quiet", "quiet", 0), detached];
    expect(landRepos(inPlace, repos)).toEqual([
      { repo: "/r/mads-nested", root: "/r/mads-nested", branch: "nested-delegates", base: "main", commits: 1 },
    ]);
  });

  it("puts the isolated session's own branch first, from the record, even before the repo state answers", () => {
    const session = { ...landed, unlanded_count: 2 };
    expect(landRepos(session, null)).toEqual([
      {
        root: "/home/user/.mads/session-worktrees/session-isolated",
        branch: "mads/session/session-isolated",
        base: "main",
        commits: 2,
      },
    ]);
    // Its own worktree in the repo state isn't counted twice.
    const own = structuredClone(fixtureIsolatedRepoState);
    own.worktree.to_land = 2;
    expect(landRepos(session, [own, linked("/r/mads-nested", "nested-delegates", 1)]).map((r) => r.commits)).toEqual([2, 1]);
  });

  it("makes Land the next step for a session working in place with a linked worktree to land", () => {
    const repos = [main, linked("/r/mads-nested", "nested-delegates", 1)];
    expect(workState(inPlace, { land: landRepos(inPlace, repos) })).toEqual({ kind: "land", commits: 1, base: "main", repos: 1 });
  });

  it("sums every branch's work to land, with a base only when they share one", () => {
    const session = { ...landed, unlanded_count: 2 };
    const repos = [linked("/r/a", "a", 1), linked("/r/b", "b", 3, "release")];
    expect(workState(session, { land: landRepos(session, repos) })).toEqual({ kind: "land", commits: 6, base: undefined, repos: 3 });
  });

  it("keeps Commit first, then Land, then Push — and no Push beside Commit while a linked branch waits to land", () => {
    const dirty = { ...inPlace, uncommitted_count: 1 };
    const repos = [{ ...main, worktree: { ...main.worktree, ahead: 2 } }, linked("/r/mads-nested", "nested-delegates", 1)];
    const state = workState(dirty, { push: pushFacts(dirty, repos), land: landRepos(dirty, repos) });
    expect(state).toEqual({ kind: "commit", files: 1, thenLand: 1, repos: 1 });
    const clean = workState(inPlace, { push: pushFacts(inPlace, repos), land: landRepos(inPlace, repos) });
    expect(clean?.kind).toBe("land");
  });
});
