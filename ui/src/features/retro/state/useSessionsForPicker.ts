import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../../router/client";
import { subscribeToEvents } from "../../../router/events";
import type { Session } from "../../../router/types";

const SESSION_EVENT_TYPES = [
  "session.created",
  "session.renamed",
  "session.activity_changed",
  "session.updated",
  "session.deleted",
  "session.crashed",
];

export interface UseSessionsForPicker {
  sessions: Session[];
  loading: boolean;
  error: string | null;
}

/**
 * Fetch-once + live session list for the retro picker. Duplicates
 * useSessionsList's shape rather than importing across feature-module
 * boundaries — no sibling feature module imports another's state today, see
 * HANDOFF §4/§11.
 */
export function useSessionsForPicker(): UseSessionsForPicker {
  const [sessions, setSessions] = useState<Session[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const mounted = useRef(true);

  const refetch = useCallback(async () => {
    try {
      const list = await router.listSessions();
      if (mounted.current) setSessions(list);
    } catch (err) {
      if (mounted.current) setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (mounted.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    void refetch();
    const unsubscribe = subscribeToEvents({ types: SESSION_EVENT_TYPES }, () => {
      void refetch();
    });
    return () => {
      mounted.current = false;
      unsubscribe();
    };
  }, [refetch]);

  return { sessions, loading, error };
}
