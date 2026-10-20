import { useState } from "react";
import { Button } from "../../components/Button/Button";
import type { Note } from "../../router/types";
import type { PipelineClient } from "../client/pipelineClient";
import styles from "./BlockedTaskFeedback.module.css";

const NOT_SUPPORTED = "not supported by the current pipeline engine";

export interface BlockedTaskFeedbackProps {
  taskId: string;
  notes: Note[];
  client: PipelineClient;
}

// A blocked task's review surface in TaskDetailSheet: the blocker and
// observation notes, plus Retry/Reassign.
export function BlockedTaskFeedback({ taskId, notes, client }: BlockedTaskFeedbackProps) {
  const [retrying, setRetrying] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const feedback = notes.filter((n) => n.type === "blocker" || n.type === "observation");

  async function handleRetry() {
    setError(null);
    setRetrying(true);
    try {
      // The retried task's task.status_changed event drives the refetch
      // that flips it out of "blocked" wherever it's shown — no manual
      // refetch needed here.
      await client.retryTask(taskId);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setRetrying(false);
    }
  }

  return (
    <>
      <ul className={styles.feedback}>
        {feedback.map((note, i) => (
          <li key={note.id ?? i} className={styles.note} data-type={note.type}>
            {note.content}
          </li>
        ))}
      </ul>
      <div className={styles.actions}>
        <Button variant="secondary" size="sm" disabled={retrying} onClick={() => void handleRetry()}>
          Retry
        </Button>
        <Button variant="secondary" size="sm" disabled title={NOT_SUPPORTED}>
          Reassign
        </Button>
      </div>
      {error && <div className={styles.error}>{error}</div>}
    </>
  );
}
