import type { Session } from "./types";

/**
 * Where a session belongs, in the one form the rail card and the
 * conversation header both print: the workspace's name when the session has
 * a workspace, otherwise the basename of the folder the user chose.
 *
 * The folder is Session.dir — what the Launchpad asked for — falling back to
 * cwd only for a session that is not isolated. An isolated session's cwd is
 * inside its own worktree (`…/session-worktrees/ses_…`), so its basename is
 * the session id: a fact about the plumbing, not about which project this is.
 */
export interface SessionLocation {
  /** What to print: workspace name, else folder basename. Absent when the record names no place at all. */
  name?: string;
  /** The folder the user chose (absolute, as recorded) — for tooltips. */
  folder?: string;
  /** An isolated session's own worktree — where the agent actually runs. */
  worktree?: string;
}

/**
 * Whether a recorded directory names a place at all. An isolation:current_dir
 * session stores cwd "." — the absence of a chosen directory — and a bare "."
 * is junk, not a location.
 */
export function isLocation(dir: string | undefined): dir is string {
  const trimmed = dir?.trim();
  return Boolean(trimmed) && trimmed !== "." && trimmed !== "./";
}

/** The last path segment, ignoring a trailing slash ("/" itself stays "/"). */
export function pathBasename(path: string): string {
  const trimmed = path.length > 1 ? path.replace(/\/+$/, "") : path;
  const slash = trimmed.lastIndexOf("/");
  return slash >= 0 && trimmed.length > 1 ? trimmed.slice(slash + 1) : trimmed;
}

/**
 * `workspaceName` is the already-resolved name (Session.workspace is an
 * id-or-name reference; the caller resolves it through router/workspaceRef.ts
 * or the rail's workspace facts). Passing nothing means "no workspace to
 * show", and the folder answers instead.
 */
export function sessionLocation(session: Session, workspaceName?: string): SessionLocation {
  const worktree = session.worktree_dir || undefined;
  const folder = isLocation(session.dir)
    ? session.dir
    : !worktree && isLocation(session.cwd)
      ? session.cwd
      : undefined;
  return {
    name: workspaceName || (folder ? pathBasename(folder) : undefined),
    folder,
    worktree,
  };
}
