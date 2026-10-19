import { Chip } from "../../components/Chip/Chip";
import { formatElapsed, spanMs } from "../../tasks/RunBar/runBarFormat";
import type { StageAttempt } from "../state/usePipelineRun";
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

/**
 * Every attempt in order: a status dot (colour for the eye, hidden text for
 * screen readers), the stage name with `#N` for a repeat attempt, and the
 * attempt's length. A failed attempt's reason sits underneath.
 */
export function StageTimeline({ attempts, onSelect, selectedIndex, now }: StageTimelineProps) {
  if (attempts.length === 0) {
    return <p className={styles.empty}>No stages started yet.</p>;
  }

  return (
    <ol className={styles.timeline} data-testid="stage-timeline">
      {attempts.map((attempt, i) => {
        const duration = attemptDuration(attempt, now);
        const retries = attempt.retries ?? 0;
        const name = (
          <>
            <span className={styles.dot} data-status={attempt.status} aria-hidden="true" />
            <span className={styles.srOnly}>{attempt.status}: </span>
            <span className={styles.name} title={attempt.stage}>
              {attempt.stage}
            </span>
            {attempt.attempt > 1 && onSelect && <span className={styles.attemptNo}>#{attempt.attempt}</span>}
          </>
        );
        return (
          <li
            key={i}
            className={[styles.entry, onSelect && styles.selectable, i === selectedIndex && styles.selected]
              .filter(Boolean)
              .join(" ")}
            data-testid="stage-timeline-entry"
            data-status={attempt.status}
          >
            <div className={styles.row}>
              {onSelect ? (
                <button
                  type="button"
                  className={styles.stageButton}
                  aria-pressed={i === selectedIndex}
                  aria-label={`${attempt.stage}${attempt.attempt > 1 ? ` #${attempt.attempt}` : ""}, ${attempt.status}`}
                  onClick={() => onSelect(i)}
                >
                  {name}
                </button>
              ) : (
                <span className={styles.stage}>{name}</span>
              )}
              {attempt.attempt > 1 && !onSelect && <Chip tone="warn">loop-back {attempt.attempt}</Chip>}
              {retries > 0 &&
                (onSelect ? (
                  <span className={styles.retries} title={`retried ×${retries}`} aria-label={`retried ×${retries}`}>
                    ↻{retries}
                  </span>
                ) : (
                  <Chip tone="info">retried ×{retries}</Chip>
                ))}
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
