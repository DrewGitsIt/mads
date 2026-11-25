import { useMemo, useSyncExternalStore } from "react";
import { getSessionsSnapshot, subscribeSessions } from "../../router/sessionsStore";
import styles from "./UsedBySessions.module.css";

export interface UsedBySessionsProps {
  workspaceId: string;
}

/**
 * Which sessions use this workspace — no server-side field for the reverse
 * direction, so this filters the shared session list (router/sessionsStore)
 * client-side (same precedent as the Tasks tab's Queued-display rule).
 */
export function UsedBySessions({ workspaceId }: UsedBySessionsProps) {
  const { sessions: all, loading, error } = useSyncExternalStore(
    subscribeSessions,
    getSessionsSnapshot,
  );
  const sessions = useMemo(
    () => all.filter((s) => s.workspace === workspaceId),
    [all, workspaceId],
  );

  if (loading) return null;
  // A failed fetch with nothing loaded yet isn't "no sessions": say nothing
  // rather than claim the workspace is unused. The store retries on its own.
  // (Once a list has loaded, a later failure keeps showing it.)
  const unknown = error !== null && all.length === 0;

  return (
    <div className={styles.field}>
      <span className={styles.label}>Used by</span>
      {unknown ? (
        <div className={styles.empty}>Couldn't load sessions.</div>
      ) : sessions.length === 0 ? (
        <div className={styles.empty}>No sessions currently use this workspace.</div>
      ) : (
        <ul className={styles.list}>
          {sessions.map((session) => (
            <li key={session.id}>{session.name}</li>
          ))}
        </ul>
      )}
    </div>
  );
}
