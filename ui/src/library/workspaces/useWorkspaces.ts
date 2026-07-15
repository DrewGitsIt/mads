import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../router/client";
import { subscribeToEvents } from "../../router/events";
import type { Workspace } from "../../router/types";

const WORKSPACE_EVENT_TYPES = ["workspace.created", "workspace.updated", "workspace.deleted"];

export interface UseWorkspaces {
  workspaces: Workspace[];
  loading: boolean;
  error: string | null;
  createWorkspace: (workspace: Partial<Workspace>) => Promise<Workspace>;
  updateWorkspace: (workspace: Workspace) => Promise<Workspace>;
  deleteWorkspace: (id: string) => Promise<void>;
}

/** Owns the shared workspace list — fetched once, kept live via the workspace.* SSE domain. */
export function useWorkspaces(): UseWorkspaces {
  const [workspaces, setWorkspaces] = useState<Workspace[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const mounted = useRef(true);

  const refetch = useCallback(async () => {
    try {
      const list = await router.listWorkspaces();
      if (mounted.current) setWorkspaces(list);
    } catch (err) {
      if (mounted.current) setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (mounted.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    void refetch();
    const unsubscribe = subscribeToEvents({ types: WORKSPACE_EVENT_TYPES }, () => {
      void refetch();
    });
    return () => {
      mounted.current = false;
      unsubscribe();
    };
  }, [refetch]);

  const createWorkspace = useCallback(async (workspace: Partial<Workspace>) => {
    return router.createWorkspace(workspace);
  }, []);

  const updateWorkspace = useCallback(async (workspace: Workspace) => {
    return router.updateWorkspace(workspace);
  }, []);

  const deleteWorkspace = useCallback(async (id: string) => {
    await router.deleteWorkspace(id);
  }, []);

  return { workspaces, loading, error, createWorkspace, updateWorkspace, deleteWorkspace };
}
