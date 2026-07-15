import { useEffect, useState } from "react";
import type { RouterClient } from "../router/client";
import type { FileChange } from "../router/types";
import { Chip } from "../components/Chip/Chip";
import styles from "./RailChangesList.module.css";

export interface RailChangesListProps {
  sessionId: string;
  routerClient: RouterClient;
  onOpenFile?: (path: string) => void;
}

const CHANGE_BADGE: Record<FileChange["change"], string> = {
  added: "A",
  modified: "M",
  deleted: "D",
};

/**
 * One-shot fetch, no live file.changed subscription (§7 — not required by
 * this task's acceptance criteria; only task.* streaming is tested).
 */
export function RailChangesList({ sessionId, routerClient, onOpenFile }: RailChangesListProps) {
  const [changes, setChanges] = useState<FileChange[]>([]);

  useEffect(() => {
    routerClient.getSessionChanges(sessionId).then(setChanges);
  }, [routerClient, sessionId]);

  return (
    <div className={styles.list}>
      {changes.map((change) => (
        <div
          key={change.id}
          className={styles.row}
          role={onOpenFile ? "button" : undefined}
          tabIndex={onOpenFile ? 0 : undefined}
          onClick={onOpenFile ? () => onOpenFile(change.path) : undefined}
        >
          <span className={styles.badge} data-change={change.change}>
            {CHANGE_BADGE[change.change]}
          </span>
          <span className={styles.path}>{change.path}</span>
          <Chip tone={change.attributed ? "ok" : "neutral"}>
            {change.attributed ? (change.actor ?? "attributed") : "unattributed"}
          </Chip>
        </div>
      ))}
    </div>
  );
}
