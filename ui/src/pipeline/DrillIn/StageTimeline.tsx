import { Chip, type ChipTone } from "../../components/Chip/Chip";
import { formatElapsed, spanMs } from "../../tasks/RunBar/runBarFormat";
import type { StageAttempt, StageAttemptStatus } from "../state/usePipelineRun";
import styles from "./StageTimeline.module.css";

export interface StageTimelineProps {
  attempts: StageAttempt[];
  /** Makes each entry a button that picks that attempt (the lens). */
  onSelect?: (index: number) => void;
  selectedIndex?: number;
  /** The clock for an open attempt's time so far (a live run). Without it, open attempts show no time. */
  now?: number;
}

/**
 * An attempt's length as `m:ss`. An open attempt shows its time so far only
 * when `now` is given (the run is live); skipped ones and attempts folded
 * from events without timestamps show none.
 */
export function attemptDuration(attempt: StageAttempt, now?: number): string | undefined {
  if (!attempt.startedAt || attempt.status === "skipped") return undefined;
  if (attempt.endedAt) return formatElapsed(spanMs(attempt.startedAt, attempt.endedAt, 0));
  if (attempt.status === "started" && now !== undefined)
    return formatElapsed(spanMs(attempt.startedAt, undefined, now));
  return undefined;
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

export function StageTimeline({ attempts, onSelect, selectedIndex, now }: StageTimelineProps) {
  if (attempts.length === 0) {
    return <p className={styles.empty}>No stages started yet.</p>;
  }

  return (
    <ol className={styles.timeline} data-testid="stage-timeline">
      {attempts.map((attempt, i) => {
        const duration = attemptDuration(attempt, now);
        return (
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
              {!!attempt.retries && attempt.retries > 0 && <Chip tone="info">retried ×{attempt.retries}</Chip>}
              <Chip tone={statusTone(attempt.status)}>{attempt.status}</Chip>
              {duration && (
                <span className={styles.duration}>
                  {attempt.status === "started" ? "live " : ""}
                  {duration}
                </span>
              )}
            </div>
            {attempt.reason && (
              <p className={styles.reason}>
                {onSelect && attempt.status === "failed" ? "↺ " : ""}
                {attempt.reason}
              </p>
            )}
          </li>
        );
      })}
    </ol>
  );
}
