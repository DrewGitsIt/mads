import { useState } from "react";
import { FilesTab } from "./FilesTab";
import { FakeRouterClient } from "../router/fixtures";
import styles from "./FilesDemo.module.css";

const client = new FakeRouterClient();

const DEMO_SESSIONS: { id: string; label: string; taskId?: string }[] = [
  { id: "session-files-clean", label: "session-files-clean (clean changes)", taskId: "tsk_retry-backoff" },
  {
    id: "session-files-observed",
    label: "session-files-observed (unattributed observation)",
    taskId: "tsk_flaky-metrics",
  },
  { id: "session-files-conflict", label: "session-files-conflict (conflicted merge)", taskId: "tsk_contract-rename" },
];

export function FilesDemo() {
  const [sessionId, setSessionId] = useState(DEMO_SESSIONS[0].id);
  const session = DEMO_SESSIONS.find((s) => s.id === sessionId)!;

  return (
    <div className={styles.demo}>
      <div className={styles.picker}>
        {DEMO_SESSIONS.map((s) => (
          <button
            key={s.id}
            type="button"
            className={s.id === sessionId ? styles.active : ""}
            onClick={() => setSessionId(s.id)}
          >
            {s.label}
          </button>
        ))}
      </div>
      <div className={styles.host}>
        <FilesTab key={sessionId} sessionId={session.id} taskId={session.taskId} routerClient={client} />
      </div>
    </div>
  );
}
