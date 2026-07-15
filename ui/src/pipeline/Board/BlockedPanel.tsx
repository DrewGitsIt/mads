import { useState } from "react";
import { Button } from "../../components/Button/Button";
import { Card } from "../../components/Card/Card";
import { Chip } from "../../components/Chip/Chip";
import type { Task } from "../../router/types";
import type { PipelineClient } from "../client/pipelineClient";
import type { TaskRunState } from "../state/usePipelineRun";
import styles from "./BlockedPanel.module.css";

const NOT_SUPPORTED = "not supported by the current pipeline engine";

export interface BlockedPanelProps {
  tasks: Task[];
  taskRunState: Record<string, TaskRunState>;
  client: PipelineClient;
  onSelectTask?: (taskId: string) => void;
}

// Deliberately separate from StageBoard's columns — a block should be a
// hard-to-miss element, not a card quietly sitting on the board (HANDOFF.md §2).
export function BlockedPanel({ tasks, taskRunState, client, onSelectTask }: BlockedPanelProps) {
  const [retryingId, setRetryingId] = useState<string | null>(null);
  const [errorId, setErrorId] = useState<string | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  const blocked = tasks.filter((task) => task.status === "blocked");
  if (blocked.length === 0) return null;

  async function handleRetry(taskId: string) {
    setErrorId(null);
    setRetryingId(taskId);
    try {
      // The retried task's task.status_changed event (emitted by the
      // Data Service on the Router's /tasks/:id/retry) drives the refetch
      // that flips it out of this panel — no manual refetch needed here
      // (usePipelineRun already subscribes, HANDOFF.md §2).
      await client.retryTask(taskId);
    } catch (err) {
      setErrorId(taskId);
      setErrorMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setRetryingId(null);
    }
  }

  return (
    <div className={styles.panel}>
      <div className={styles.heading}>
        <Chip tone="bad">{blocked.length} blocked</Chip>
      </div>
      {blocked.map((task) => {
        const stage = taskRunState[task.id]?.currentStage;
        const notes = (task.notes ?? []).filter((n) => n.type === "blocker" || n.type === "observation");
        return (
          <Card key={task.id} className={styles.card}>
            <button
              type="button"
              className={styles.header}
              onClick={onSelectTask ? () => onSelectTask(task.id) : undefined}
              disabled={!onSelectTask}
            >
              <span className={styles.title}>{task.title}</span>
              {stage && <span className={styles.stage}>blocked at {stage}</span>}
            </button>
            <ul className={styles.feedback}>
              {notes.map((note, i) => (
                <li key={note.id ?? i} className={styles.note} data-type={note.type}>
                  {note.content}
                </li>
              ))}
            </ul>
            <div className={styles.actions}>
              <Button
                variant="secondary"
                size="sm"
                disabled={retryingId === task.id}
                onClick={() => void handleRetry(task.id)}
              >
                Retry
              </Button>
              <Button variant="secondary" size="sm" disabled title={NOT_SUPPORTED}>
                Reassign
              </Button>
            </div>
            {errorId === task.id && errorMessage && <div className={styles.error}>{errorMessage}</div>}
          </Card>
        );
      })}
    </div>
  );
}
