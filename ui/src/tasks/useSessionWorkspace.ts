import { useEffect, useState } from "react";
import type { RouterClient } from "../router/client";
import { subscribeToEvents } from "../router/events";
import type { Workspace } from "../router/types";

const WORKSPACE_EVENT_TYPES = ["workspace.created", "workspace.updated", "workspace.deleted"];

/**
 * Resolves the Workspace a session belongs to. Session.workspace is a name,
 * not an id (router/types.ts), so this chains getSession -> listWorkspaces
 * and matches by name. Null once resolved if the session has no workspace.
 */
export function useSessionWorkspace(routerClient: RouterClient, sessionId: string): Workspace | null {
  const [workspace, setWorkspace] = useState<Workspace | null>(null);

  useEffect(() => {
    let cancelled = false;

    async function load() {
      const session = await routerClient.getSession(sessionId);
      if (!session.workspace) {
        if (!cancelled) setWorkspace(null);
        return;
      }
      const workspaces = await routerClient.listWorkspaces();
      if (!cancelled) {
        setWorkspace(workspaces.find((w) => w.name === session.workspace) ?? null);
      }
    }

    void load();

    const unsubscribe = subscribeToEvents({ types: WORKSPACE_EVENT_TYPES }, () => {
      void load();
    });

    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [routerClient, sessionId]);

  return workspace;
}
