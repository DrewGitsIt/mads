import type { Commit, FileChange } from "../../router/types";

/**
 * Attribution taxonomy the Files tab renders (ui/src/files/HANDOFF.md §4).
 * FileChange and Commit are two separate observation shapes with no join
 * key — a change is attributed (actor did it, or the actor field is unset
 * because worktree isolation attributes everything, per
 * `contracts.AttributedChange`) or unattributed (an observed change nobody
 * confirmed); a commit is linked via trailer/session-context/heuristic, or
 * flagged orphan.
 */
export type AttributionKind =
  | "actor"
  | "trailer"
  | "session"
  | "heuristic"
  | "orphan"
  | "unattributed";

export function attributionForChange(fc: FileChange): AttributionKind {
  return fc.attributed ? "actor" : "unattributed";
}

export function attributionForCommit(c: Commit): AttributionKind {
  if (c.orphan) return "orphan";
  if (c.linked_via === "trailer") return "trailer";
  if (c.linked_via === "session_context") return "session";
  if (c.linked_via === "heuristic") return "heuristic";
  // Defensive fallback; shouldn't occur if the server sets `orphan`
  // correctly whenever `linked_via` is empty.
  return "unattributed";
}
