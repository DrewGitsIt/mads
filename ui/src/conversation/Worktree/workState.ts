import type { PushTarget, Session, SessionRepoState } from "../../router/types";

/** One repo a push would send commits from (repo state, COMMIT-LAND-PUSH D10). */
export interface PushRepo {
  root: string;
  primary?: boolean;
  isolated?: boolean;
  /** An isolated session's own repo pushes its base once landed; every other repo its branch. */
  target: PushTarget;
  /** Commits that push would publish, copies of commits already on the remote left out. */
  commits: number;
}

/**
 * What the session's next push would send, from the repo state store —
 * never the session record, whose copy went stale whenever another session
 * moved the shared branch. Undefined while the store hasn't answered:
 * unknown, so nothing offers Push yet.
 */
export interface PushFacts {
  /** Summed over `repos`. */
  commits: number;
  /** The repos with something to push, in the session's order (its own first). */
  repos: PushRepo[];
  /** Isolated, before Land: the session branch's own commits a push for a PR would publish. */
  branchAhead?: number;
}

/**
 * One branch Land would squash onto its base (COMMIT-LAND-PUSH D12): the
 * session's own isolated worktree (from the record), or a linked worktree
 * an agent made (from the repo state).
 */
export interface LandRepo {
  /** LandRequest.repo: the worktree's root; undefined for the session's own isolated worktree. */
  repo?: string;
  root: string;
  branch?: string;
  base?: string;
  /** Commits to land, copies of commits already on the base left out. */
  commits: number;
}

/** What the repo state says beyond the record: what Push and Land would act on. */
export interface RepoFacts {
  push?: PushFacts;
  /** landRepos(); left out, only the isolated session's own branch (the record's count). */
  land?: LandRepo[];
}

/** What Push would send next, alone or beside Commit (D11). */
export interface PushNext {
  commits: number;
  /** The session's own repo's target; other repos push their branch. */
  target: PushTarget;
  repos: number;
}

/**
 * The session's next step with its work (docs/SESSION-WORKTREES-DESIGN.md
 * D4), read straight off the record's counts and, for Push, the repo state
 * — never computed here, never per render. Shared by the dock's
 * Commit/Land/Push control, the Changes chip and the rail marker so they
 * can't disagree.
 */
export type WorkState =
  | {
      kind: "commit";
      files: number;
      /** Commits waiting to land after this one (isolated only). */
      thenLand: number;
      /** Repos those files are in (COMMIT-LAND-PUSH D3); 1 unless the record breaks them down. */
      repos: number;
      /**
       * Commits to push as well, shown beside Commit (D11): files left out of
       * a commit on purpose shouldn't hide the push. Never while something
       * waits to land — that push would come after Land.
       */
      push?: PushNext;
    }
  /** Commits to land over every landable branch; `base` when they all land into the same one. */
  | { kind: "land"; commits: number; base?: string; repos: number }
  /** Files off the session's change list are changed in its folder: Commit can't take them, Land would refuse. */
  | { kind: "outside"; files: number }
  /**
   * Committed (and landed) with commits the remote lacks (COMMIT-LAND-PUSH
   * D5): an isolated session pushes its base, in place its branch.
   */
  | ({ kind: "push" } & PushNext)
  | null;

/** An isolated session has its own branch and a recorded base to land into. */
export function isIsolated(session: Session): boolean {
  return Boolean(session.worktree_dir && session.worktree_branch && session.worktree_base);
}

/**
 * How many of the session's repos have uncommitted files
 * (docs/COMMIT-LAND-PUSH-DESIGN.md D1). The record lists repos only when
 * the work spans more than the folder's, so a bare count means one.
 */
export function uncommittedRepos(session: Session): number {
  const listed = (session.repos ?? []).filter((r) => (r.uncommitted_count ?? 0) > 0).length;
  if (listed > 0) return listed;
  return (session.uncommitted_count ?? 0) > 0 ? 1 : 0;
}

/** Commits an isolated repo still has to land, from the record (its own branch is the session's). */
function leftToLand(session: Session, repo: SessionRepoState): number {
  const listed = session.repos?.find((r) => r.root === repo.root);
  if (listed) return listed.unlanded_count ?? 0;
  return repo.primary ? (session.unlanded_count ?? 0) : 0;
}

/**
 * What Push would send, per repo (D10): a repo worked in place pushes its
 * checked-out branch, `worktree.ahead`; an isolated session's repo pushes
 * its base from the user's checkout, `base_worktree.ahead`, once nothing is
 * left to land — before that, its own branch's `worktree.ahead` is only the
 * optional push for a PR. Undefined while the repos are unknown.
 */
export function pushFacts(session: Session, repos: SessionRepoState[] | null | undefined): PushFacts | undefined {
  if (!repos) return undefined;
  const out: PushRepo[] = [];
  let branchAhead: number | undefined;
  for (const r of repos) {
    if (r.isolated) {
      if (leftToLand(session, r) > 0) {
        branchAhead = r.worktree.ahead;
        continue;
      }
      const ahead = r.base_worktree?.ahead ?? 0;
      if (ahead > 0) out.push({ root: r.root, primary: r.primary, isolated: true, target: "base", commits: ahead });
      continue;
    }
    if (r.worktree.ahead > 0) {
      out.push({ root: r.root, primary: r.primary, target: "branch", commits: r.worktree.ahead });
    }
  }
  return { commits: out.reduce((n, r) => n + r.commits, 0), repos: out, branchAhead };
}

/**
 * The branches Land would act on, the session's own first: an isolated
 * session's branch with commits its base lacks (the record's unlanded
 * count), then each linked worktree in its repo set — not a main checkout,
 * on a branch — with work to land. `repos` unknown: the first only.
 */
export function landRepos(session: Session, repos: SessionRepoState[] | null | undefined): LandRepo[] {
  const out: LandRepo[] = [];
  const own = isIsolated(session) ? (session.unlanded_count ?? 0) : 0;
  if (own > 0) {
    out.push({
      root: session.worktree_dir!,
      branch: session.worktree_branch,
      base: session.worktree_base,
      commits: own,
    });
  }
  for (const r of repos ?? []) {
    const wt = r.worktree;
    if (r.isolated || wt.main || wt.detached || !wt.branch || (wt.to_land ?? 0) === 0) continue;
    out.push({ repo: r.root, root: r.root, branch: wt.branch, base: wt.base, commits: wt.to_land! });
  }
  return out;
}

function pushNext(push: PushFacts | undefined): PushNext | undefined {
  if (!push || push.commits === 0) return undefined;
  const own = push.repos.find((r) => r.primary) ?? push.repos[0];
  return { commits: push.commits, target: own.target, repos: push.repos.length };
}

/**
 * The next step: Commit, then Land, then Push. `facts` come from the repo
 * state store (pushFacts, landRepos); left out (the rail), or unknown while
 * the store loads, Push is never offered and Land is the isolated session's
 * own branch only.
 */
export function workState(session: Session, facts: RepoFacts = {}): WorkState {
  const uncommitted = session.uncommitted_count ?? 0;
  const land = facts.land ?? landRepos(session, null);
  const unlanded = land.reduce((n, r) => n + r.commits, 0);
  const outside = isIsolated(session) ? (session.outside_count ?? 0) : 0;
  const next = pushNext(facts.push);
  if (uncommitted > 0) {
    return {
      kind: "commit",
      files: uncommitted,
      thenLand: unlanded,
      repos: uncommittedRepos(session),
      ...(next && unlanded === 0 ? { push: next } : {}),
    };
  }
  // Land's gate is the whole folder's git status, not just the session's
  // files: offering Land over changes Commit can't take only earns a refusal.
  if (outside > 0) return { kind: "outside", files: outside };
  if (unlanded > 0) {
    const bases = new Set(land.map((r) => r.base));
    return {
      kind: "land",
      commits: unlanded,
      base: bases.size === 1 ? land[0].base : undefined,
      repos: land.length,
    };
  }
  // Push is last: what it sends is what Commit and Land made.
  if (next) return { kind: "push", ...next };
  return null;
}
