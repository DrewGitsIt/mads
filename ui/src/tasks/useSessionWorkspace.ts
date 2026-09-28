import { useEffect, useMemo, useState } from "react";
import type { RouterClient } from "../router/client";
import { subscribeToEvents } from "../router/events";
import type { Workspace } from "../router/types";
import { resolveWorkspaceRef, workspaceIndex } from "../router/workspaceRef";

const WORKSPACE_EVENT_TYPES = ["workspace.created", "workspace.updated", "workspace.deleted"];

/**
 * Resolves a `Session.workspace` reference — an id-or-name, never a printable
 * name on its own (router/workspaceRef.ts is the one place that resolves one) —
 * against the live workspace list. Null while nothing is referenced, and null
 * once loaded if the reference no longer names a workspace.
 *
 * For a caller that already holds the session; useSessionWorkspace is the same
 * thing for one that only holds a session id. The list is fetched and
 * subscribed on mount rather than when a reference arrives: the reference
 * typically arrives with an awaited session, and a subscription opened from
 * that continuation would attach outside the mount it belongs to.
 */
export function useWorkspaceForRef(
  routerClient: RouterClient,
  ref: string | undefined,
): Workspace | null {
  const [workspaces, setWorkspaces] = useState<Workspace[]>([]);

  useEffect(() => {
    let cancelled = false;

    async function load() {
      const list = await routerClient.listWorkspaces();
      if (!cancelled) setWorkspaces(list);
    }

    void load();

    const unsubscribe = subscribeToEvents({ types: WORKSPACE_EVENT_TYPES }, () => {
      void load();
    });

    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [routerClient]);

  return useMemo(
    () => resolveWorkspaceRef(ref, workspaceIndex(workspaces)) ?? null,
    [ref, workspaces],
  );
}

/**
 * The Workspace a session belongs to, for a caller that has only its id: this
 * chains getSession -> useWorkspaceForRef.
 */
export function useSessionWorkspace(routerClient: RouterClient, sessionId: string): Workspace | null {
  const [ref, setRef] = useState<string>();

  useEffect(() => {
    let cancelled = false;
    void routerClient.getSession(sessionId).then((session) => {
      if (!cancelled) setRef(session.workspace);
    });
    return () => {
      cancelled = true;
    };
  }, [routerClient, sessionId]);

  return useWorkspaceForRef(routerClient, ref);
}
