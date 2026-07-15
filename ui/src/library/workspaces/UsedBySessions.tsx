import { useEffect, useRef, useState } from "react";
import { routerGet } from "../../router/client";
import type { Session } from "../../router/types";
import styles from "./UsedBySessions.module.css";

export interface UsedBySessionsProps {
  workspaceId: string;
}

/**
 * Which sessions use this workspace — no server-side field for the reverse
 * direction, so this filters GET /sessions client-side (same precedent as
 * the Tasks tab's Queued-display rule).
 */
export function UsedBySessions({ workspaceId }: UsedBySessionsProps) {
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    routerGet<Session[]>("/sessions")
      .then((all) => {
        if (mounted.current) setSessions(all.filter((s) => s.workspace === workspaceId));
      })
      .catch(() => {
        if (mounted.current) setSessions([]);
      });
    return () => {
      mounted.current = false;
    };
  }, [workspaceId]);

  if (sessions === null) return null;

  return (
    <div className={styles.field}>
      <span className={styles.label}>Used by</span>
      {sessions.length === 0 ? (
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
