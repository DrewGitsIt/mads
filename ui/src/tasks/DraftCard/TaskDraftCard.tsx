import { useState } from "react";
import type { RouterClient } from "../../router/client";
import type { Task } from "../../router/types";
import { Draft, DraftChip } from "../../components/Draft/Draft";
import { Button } from "../../components/Button/Button";
import styles from "./TaskDraftCard.module.css";

export interface TaskDraftCardProps {
  task: Task;
  routerClient: RouterClient;
  /** Edit opens TaskDetailSheet in edit mode without confirming — confirm stays a separate explicit action (§4). */
  onEdit: (task: Task) => void;
  onConfirmed?: (task: Task) => void;
  onDiscarded?: (taskId: string) => void;
}

export function TaskDraftCard({
  task,
  routerClient,
  onEdit,
  onConfirmed,
  onDiscarded,
}: TaskDraftCardProps) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleConfirm() {
    setBusy(true);
    setError(null);
    try {
      await routerClient.confirmTask(task.id);
      onConfirmed?.({ ...task, draft: false });
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to confirm task");
    } finally {
      setBusy(false);
    }
  }

  async function handleDiscard() {
    setBusy(true);
    setError(null);
    try {
      await routerClient.discardTask(task.id);
      onDiscarded?.(task.id);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to discard task");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Draft className={styles.card}>
      <div className={styles.header}>
        <DraftChip />
        <span className={styles.title}>{task.title}</span>
      </div>
      {task.description && <p className={styles.description}>{task.description}</p>}
      {error && <p className={styles.error}>{error}</p>}
      <div className={styles.actions}>
        <Button size="sm" onClick={handleConfirm} disabled={busy}>
          Confirm
        </Button>
        <Button variant="secondary" size="sm" onClick={() => onEdit(task)} disabled={busy}>
          Edit
        </Button>
        <Button variant="ghost" size="sm" onClick={handleDiscard} disabled={busy}>
          Discard
        </Button>
      </div>
    </Draft>
  );
}
