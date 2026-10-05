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
  | null;

/** An isolated session has its own branch and a recorded base to land into. */
export function isIsolated(session: Session): boolean {
  return Boolean(session.worktree_dir && session.worktree_branch && session.worktree_base);
}

export function workState(session: Session): WorkState {
  const uncommitted = session.uncommitted_count ?? 0;
  const unlanded = isIsolated(session) ? (session.unlanded_count ?? 0) : 0;
  if (uncommitted > 0) return { kind: "commit", files: uncommitted, thenLand: unlanded };
  if (unlanded > 0) return { kind: "land", commits: unlanded, base: session.worktree_base! };
  return null;
}
