import { Card } from "../../components/Card/Card";
import { Chip } from "../../components/Chip/Chip";
import type { Task } from "../../router/types";
import type { TaskRunState } from "../state/usePipelineRun";
import styles from "./TaskCard.module.css";

export interface TaskCardProps {
  task: Task;
  runState?: TaskRunState;
  onClick?: () => void;
}

// The attempt badge tracks the current stage's own attempt count, not a
// run-wide total — e.g. "attempt 3" means the 3rd try at the stage the card
// is currently sitting in (loop-backs re-enter the same stage, HANDOFF.md §1c).
function currentAttempt(runState?: TaskRunState): number | undefined {
  if (!runState?.currentStage) return undefined;
  const attempts = runState.attempts.filter((a) => a.stage === runState.currentStage);
  return attempts.length > 0 ? attempts[attempts.length - 1].attempt : undefined;
}

export function TaskCard({ task, runState, onClick }: TaskCardProps) {
  const attempt = currentAttempt(runState);
  return (
    <Card className={styles.card}>
      <button type="button" className={styles.header} onClick={onClick} disabled={!onClick}>
        <span className={styles.title}>{task.title}</span>
        {attempt !== undefined && attempt > 1 && <Chip tone="warn">attempt {attempt}</Chip>}
      </button>
    </Card>
  );
}
