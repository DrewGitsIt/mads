import type { Session } from "../../router/types";

/**
 * The session's next step with its work (docs/SESSION-WORKTREES-DESIGN.md
 * D4), read straight off the record's counts — never computed here, never
 * per render. Shared by the dock's Commit/Land control and the rail marker so the two
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
    }
  | { kind: "land"; commits: number; base: string }
  /** Files off the session's change list are changed in its folder: Commit can't take them, Land would refuse. */
  | { kind: "outside"; files: number }
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

export function workState(session: Session): WorkState {
  const uncommitted = session.uncommitted_count ?? 0;
  const unlanded = isIsolated(session) ? (session.unlanded_count ?? 0) : 0;
  const outside = isIsolated(session) ? (session.outside_count ?? 0) : 0;
  if (uncommitted > 0) return { kind: "commit", files: uncommitted, thenLand: unlanded, repos: uncommittedRepos(session) };
  // Land's gate is the whole folder's git status, not just the session's
  // files: offering Land over changes Commit can't take only earns a refusal.
  if (outside > 0) return { kind: "outside", files: outside };
  if (unlanded > 0) return { kind: "land", commits: unlanded, base: session.worktree_base! };
  return null;
}
