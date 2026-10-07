import { useEffect, useState } from "react";
import type { RouterClient } from "../../router/client";
import { sessionLocation } from "../../router/sessionLocation";
import type { Session } from "../../router/types";

/**
 * The branch a session working in place has open — the one fact the dock's
 * ⎇ segment needs that the record doesn't carry (only an isolated session has
 * worktree_branch). Read from GET /sessions/defaults?dir=, whose dir_branch
 * is the checkout's current branch: one cheap `git symbolic-ref`, no new
 * endpoint.
 *
 * Re-read after every turn (last_turn_at), since the agent may well have
 * switched branches. Undefined for an isolated session, a folder that isn't
 * a git checkout, a detached HEAD, or a session with no folder on record.
 */
export function useCheckoutBranch(routerClient: RouterClient, session: Session | null | undefined): string | undefined {
  const inPlace = Boolean(session && !session.worktree_dir && !session.worktree_branch);
  const folder = inPlace && session ? sessionLocation(session).folder : undefined;
  const turn = session?.last_turn_at;
  const [branch, setBranch] = useState<{ folder: string; branch?: string }>();

  useEffect(() => {
    if (!folder) return;
    let cancelled = false;
    routerClient.getSessionDefaults(undefined, folder).then(
      (d) => !cancelled && setBranch({ folder, branch: d.dir_branch || undefined }),
      () => {},
    );
    return () => {
      cancelled = true;
    };
  }, [routerClient, folder, turn]);

  // Keyed by folder, so a session switch never shows the previous one's branch.
  return folder && branch?.folder === folder ? branch.branch : undefined;
}
