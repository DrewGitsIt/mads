import type { Workspace } from "./types";

/**
 * Session.workspace is a *reference* to a workspace, not a name: the router
 * canonicalises it to the id on create (resolveSessionDefaultsSourced in
 * services/router/internal/api/sessiondefaults.go writes `sess.Workspace =
 * ws.ID`), while the registry's own lookup takes either form
 * (dataclient.Workspace(ctx, idOrName)) — so a record written by some other
 * path may still carry a name. The contract that holds on both sides is
 * "id-or-name", and this is the one place the UI turns one into a Workspace.
 *
 * Every surface that shows a workspace to the user, or indexes it for search,
 * must go through here: `wsp_b8439a998b38a07f` is not something to print.
 */
export type WorkspaceIndex = Map<string, Workspace>;

export const EMPTY_WORKSPACE_INDEX: WorkspaceIndex = new Map();

/** Indexes a workspace list by both of the keys a reference may use. */
export function workspaceIndex(workspaces: Workspace[]): WorkspaceIndex {
  const index: WorkspaceIndex = new Map();
  // Names first, then ids: should a workspace ever be *named* like another's
  // id, the id must win — it is the canonical key, and the name is the one a
  // user typed.
  for (const ws of workspaces) if (ws.name) index.set(ws.name, ws);
  for (const ws of workspaces) if (ws.id) index.set(ws.id, ws);
  return index;
}

/** The workspace a `Session.workspace` reference names, by id or by name. */
export function resolveWorkspaceRef(
  ref: string | undefined,
  index: WorkspaceIndex,
): Workspace | undefined {
  return ref ? index.get(ref) : undefined;
}

/**
 * The name to show for a reference. An unresolved reference falls back to the
 * reference itself — a workspace that has been deleted, or a list that hasn't
 * loaded yet, is better answered with a stale key than with a blank.
 */
export function workspaceNameForRef(
  ref: string | undefined,
  index: WorkspaceIndex,
): string | undefined {
  if (!ref) return undefined;
  return index.get(ref)?.name || ref;
}
