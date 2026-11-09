import { useCallback, useSyncExternalStore } from "react";
import { router } from "../../../router/client";
import { getSessionsSnapshot, refreshSessions, subscribeSessions } from "../../../router/sessionsStore";
import type { CreateSessionPayload, Session } from "../../../router/types";

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
 * Reads the shared session list — one window-wide store (router/sessionsStore)
 * however many components call this, kept live via the session.* SSE domain.
 * Event payloads vary per type and don't always carry the full session, so
 * events trigger a plain refetch, coalesced and single-flight: the bus replays
 * the whole event log on load, and a fetch per event per hook once meant
 * thousands of GET /sessions in a few seconds.
 */
export function useSessionsList(): UseSessionsList {
  const { sessions, loading, error } = useSyncExternalStore(subscribeSessions, getSessionsSnapshot);

  const createSession = useCallback(async (payload: CreateSessionPayload) => {
    const result = await router.createSession(payload);
    return result.session;
  }, []);

  return { sessions, loading, error, retry: refreshSessions, createSession };
}
