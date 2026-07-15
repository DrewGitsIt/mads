import { Chip, type ChipTone } from "../../components/Chip/Chip";
import type { StageAttempt, StageAttemptStatus } from "../state/usePipelineRun";
import styles from "./StageTimeline.module.css";

export interface StageTimelineProps {
  attempts: StageAttempt[];
}

function statusTone(status: StageAttemptStatus): ChipTone {
  switch (status) {
    case "completed":
      return "ok";
    case "failed":
      return "bad";
    case "skipped":
      return "neutral";
    default:
      return "info";
  }
}

export function StageTimeline({ attempts }: StageTimelineProps) {
  if (attempts.length === 0) {
    return <p className={styles.empty}>No stages started yet.</p>;
  }

  return (
    <ol className={styles.timeline}>
      {attempts.map((attempt, i) => (
        <li key={i} className={styles.entry}>
          <div className={styles.row}>
            <span className={styles.stage}>{attempt.stage}</span>
            {attempt.attempt > 1 && <Chip tone="warn">loop-back {attempt.attempt}</Chip>}
            <Chip tone={statusTone(attempt.status)}>{attempt.status}</Chip>
          </div>
          {attempt.reason && <p className={styles.reason}>{attempt.reason}</p>}
        </li>
      ))}
    </ol>
  );
}
