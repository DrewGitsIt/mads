import type { Session } from "../../router/types";

/**
 * The session's next step with its work (docs/SESSION-WORKTREES-DESIGN.md
 * D4), read straight off the record's counts — never computed here, never
 * per render. Shared by the header control and the rail marker so the two
 * can't disagree.
 */
export type WorkState =
  | { kind: "commit"; files: number; /** Commits waiting to land after this one (isolated only). */ thenLand: number }
  | { kind: "land"; commits: number; base: string }
  /** Files off the session's change list are changed in its folder: Commit can't take them, Land would refuse. */
  | { kind: "outside"; files: number }
  | null;

/** An isolated session has its own branch and a recorded base to land into. */
export function isIsolated(session: Session): boolean {
  return Boolean(session.worktree_dir && session.worktree_branch && session.worktree_base);
}

export function workState(session: Session): WorkState {
  const uncommitted = session.uncommitted_count ?? 0;
  const unlanded = isIsolated(session) ? (session.unlanded_count ?? 0) : 0;
  const outside = isIsolated(session) ? (session.outside_count ?? 0) : 0;
  if (uncommitted > 0) return { kind: "commit", files: uncommitted, thenLand: unlanded };
  // Land's gate is the whole folder's git status, not just the session's
  // files: offering Land over changes Commit can't take only earns a refusal.
  if (outside > 0) return { kind: "outside", files: outside };
  if (unlanded > 0) return { kind: "land", commits: unlanded, base: session.worktree_base! };
  return null;
}
