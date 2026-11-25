import { useSyncExternalStore } from "react";
import { getSessionsSnapshot, subscribeSessions } from "../../../router/sessionsStore";
import type { Session } from "../../../router/types";

export interface UseSessionsForPicker {
  sessions: Session[];
  loading: boolean;
  error: string | null;
}

/**
 * Live session list for the retro picker. Reads the window-wide store in
 * router/sessionsStore — the same one useSessionsList reads — so the picker
 * adds no GET /sessions of its own and gets the store's coalesced refetch and
 * retry. The store lives under router/ so features share it without importing
 * each other's state (HANDOFF §4/§11).
 */
export function useSessionsForPicker(): UseSessionsForPicker {
  const { sessions, loading, error } = useSyncExternalStore(subscribeSessions, getSessionsSnapshot);
  return { sessions, loading, error };
}
