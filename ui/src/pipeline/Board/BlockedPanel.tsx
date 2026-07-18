import { Card } from "../../components/Card/Card";
import { Chip } from "../../components/Chip/Chip";
import type { Task } from "../../router/types";
import type { PipelineClient } from "../client/pipelineClient";
import type { TaskRunState } from "../state/usePipelineRun";
import { BlockedTaskFeedback } from "./BlockedTaskFeedback";
import styles from "./BlockedPanel.module.css";

export interface BlockedPanelProps {
  tasks: Task[];
  taskRunState: Record<string, TaskRunState>;
  client: PipelineClient;
  onSelectTask?: (taskId: string) => void;
}

// Deliberately separate from StageBoard's columns — a block should be a
// hard-to-miss element, not a card quietly sitting on the board (HANDOFF.md §2).
export function BlockedPanel({ tasks, taskRunState, client, onSelectTask }: BlockedPanelProps) {
  const blocked = tasks.filter((task) => task.status === "blocked");
  if (blocked.length === 0) return null;

  return (
    <div className={styles.panel}>
      <div className={styles.heading}>
        <Chip tone="bad">{blocked.length} blocked</Chip>
      </div>
      {blocked.map((task) => {
        const stage = taskRunState[task.id]?.currentStage;
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
            <div className={styles.feedbackWrap}>
              <BlockedTaskFeedback taskId={task.id} notes={task.notes ?? []} client={client} />
            </div>
          </Card>
        );
      })}
    </div>
  );
}
