import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../../router/client";
import { subscribeToEvents } from "../../../router/events";
import { SESSION_EVENT_TYPES } from "../../../router/sessionEventTypes";
import type { CreateSessionPayload, Session } from "../../../router/types";

const RETRY_MIN_MS = 2_000;
const RETRY_MAX_MS = 30_000;

export interface UseSessionsList {
  sessions: Session[];
  loading: boolean;
  /** Set while the most recent fetch failed; cleared by the next success. */
  error: string | null;
  /** Refetch now (the toast's Retry); failures also retry on their own. */
  retry: () => void;
  createSession: (payload: CreateSessionPayload) => Promise<Session>;
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
  // A burst of session.* events (e.g. a close's activity_changed landing
  // right after an unrelated updated) fires refetch() multiple times
  // concurrently. Nothing guarantees those GETs resolve in request order —
  // without this guard, a slower, earlier request can resolve after a
  // faster, later one and overwrite fresh state with stale data, and
  // nothing re-triggers a fix since no further event is coming. Only the
  // most recently *issued* request is allowed to commit its result.
  const latestRequest = useRef(0);
  // A failed fetch retries on its own with backoff: the refetches are
  // event-driven, so without this a single blip (a router restart mid-refresh)
  // would leave the error standing until some unrelated session event came.
  const retryTimer = useRef<ReturnType<typeof setTimeout>>();
  const retryDelay = useRef(RETRY_MIN_MS);

  const refetch = useCallback(async () => {
    const requestId = ++latestRequest.current;
    clearTimeout(retryTimer.current);
    try {
      const list = await router.listSessions();
      if (mounted.current && requestId === latestRequest.current) {
        setSessions(list);
        setError(null);
        retryDelay.current = RETRY_MIN_MS;
      }
    } catch (err) {
      if (mounted.current && requestId === latestRequest.current) {
        setError(err instanceof Error ? err.message : String(err));
        retryTimer.current = setTimeout(() => void refetch(), retryDelay.current);
        retryDelay.current = Math.min(retryDelay.current * 2, RETRY_MAX_MS);
      }
    } finally {
      if (mounted.current && requestId === latestRequest.current) setLoading(false);
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
      clearTimeout(retryTimer.current);
      unsubscribe();
    };
  }, [refetch]);

  const createSession = useCallback(async (payload: CreateSessionPayload) => {
    const result = await router.createSession(payload);
    return result.session;
  }, []);

  const retry = useCallback(() => {
    retryDelay.current = RETRY_MIN_MS;
    void refetch();
  }, [refetch]);

  return { sessions, loading, error, retry, createSession };
}
