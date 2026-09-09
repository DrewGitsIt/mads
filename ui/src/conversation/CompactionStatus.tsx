import type { CompactStatus } from "../router/types";
import styles from "./CompactionStatus.module.css";

export interface CompactionStatusProps {
  status?: CompactStatus;
  /**
   * Clears the banner. ConversationTab wires this to drop its compactStatus
   * state: a successful "done" is additionally auto-dismissed on a timer
   * there, so this button is chiefly the escape hatch for a failed pass —
   * that one stays until the user acknowledges it.
   */
  onDismiss?: () => void;
}

/**
 * Visible status item for a native compaction pass — the Runner's
 * near-limit resume trigger (services/session/internal/runner/compact.go)
 * or the user's own "Compact now" action. Headless -p mode has no
 * interactive "resume from summary" prompt, so this is surfaced explicitly
 * rather than silently. Dumb/presentational: ConversationTab owns the
 * `compact` stream-event listener and passes the latest CompactStatus in.
 */
export function CompactionStatus({ status, onDismiss }: CompactionStatusProps) {
  if (!status) return null;

  if (status.phase === "start") {
    return (
      <div className={styles.strip} role="status">
        Compacting conversation…
      </div>
    );
  }

  return (
    <div className={[styles.strip, status.success ? styles.done : styles.failed].join(" ")} role="status">
      <span className={styles.message}>
        {status.success ? "Conversation compacted." : `Compaction failed${status.error ? `: ${status.error}` : "."}`}
      </span>
      {onDismiss && (
        <button type="button" className={styles.dismiss} onClick={onDismiss} aria-label="Dismiss">
          ×
        </button>
      )}
    </div>
  );
}
