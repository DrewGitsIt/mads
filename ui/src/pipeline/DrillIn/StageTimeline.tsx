import { Chip, type ChipTone } from "../../components/Chip/Chip";
import type { StageAttempt, StageAttemptStatus } from "../state/usePipelineRun";
import styles from "./StageTimeline.module.css";

export interface StageTimelineProps {
  attempts: StageAttempt[];
  /** Makes each entry a button that picks that attempt (the lens). */
  onSelect?: (index: number) => void;
  selectedIndex?: number;
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

export function StageTimeline({ attempts, onSelect, selectedIndex }: StageTimelineProps) {
  if (attempts.length === 0) {
    return <p className={styles.empty}>No stages started yet.</p>;
  }

  return (
    <ol className={styles.timeline} data-testid="stage-timeline">
      {attempts.map((attempt, i) => (
        <li
          key={i}
          className={[styles.entry, onSelect && styles.selectable, i === selectedIndex && styles.selected]
            .filter(Boolean)
            .join(" ")}
          data-testid="stage-timeline-entry"
        >
          <div className={styles.row}>
            {onSelect ? (
              <button
                type="button"
                className={styles.stageButton}
                aria-pressed={i === selectedIndex}
                onClick={() => onSelect(i)}
              >
                {attempt.stage}
                {attempt.attempt > 1 ? ` #${attempt.attempt}` : ""}
              </button>
            ) : (
              <span className={styles.stage}>{attempt.stage}</span>
            )}
            {attempt.attempt > 1 && !onSelect && <Chip tone="warn">loop-back {attempt.attempt}</Chip>}
            {!!attempt.retries && attempt.retries > 0 && (
              <Chip tone="info">retried ×{attempt.retries}</Chip>
            )}
            <Chip tone={statusTone(attempt.status)}>{attempt.status}</Chip>
          </div>
          {attempt.reason && (
            <p className={styles.reason}>
              {onSelect && attempt.status === "failed" ? "↺ " : ""}
              {attempt.reason}
            </p>
          )}
        </li>
      ))}
    </ol>
  );
}
