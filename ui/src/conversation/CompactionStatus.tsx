import type { CompactStatus } from "../router/types";
import styles from "./CompactionStatus.module.css";

export interface CompactionStatusProps {
  status?: CompactStatus;
}

/**
 * Visible status item for a native compaction pass the Runner requested on
 * resume (services/session/internal/runner/compact.go) — headless -p mode
 * has no interactive "resume from summary" prompt, so this is surfaced
 * explicitly rather than silently. Dumb/presentational: ConversationTab owns
 * the `compact` stream-event listener and passes the latest CompactStatus in.
 */
export function CompactionStatus({ status }: CompactionStatusProps) {
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
      {status.success ? "Conversation compacted." : `Compaction failed${status.error ? `: ${status.error}` : "."}`}
    </div>
  );
}
