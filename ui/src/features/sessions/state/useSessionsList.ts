import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../../router/client";
import { subscribeToEvents } from "../../../router/events";
import { SESSION_EVENT_TYPES } from "../../../router/sessionEventTypes";
import type { Session } from "../../../router/types";

export interface UseSessionsList {
  sessions: Session[];
  loading: boolean;
  error: string | null;
  createSession: (payload: Partial<Session>) => Promise<Session>;
}

/**
 * Owns the shared session list — fetched once, then kept live via the
 * session.* SSE domain. Event payloads vary per type and don't always carry
 * the full session, so any session.* event triggers a plain refetch rather
 * than per-event payload parsing.
 */
export function useSessionsList(): UseSessionsList {
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

  const createSession = useCallback(async (payload: Partial<Session>) => {
    const result = await router.createSession(payload);
    return result.session;
  }, []);

  return { sessions, loading, error, createSession };
}
