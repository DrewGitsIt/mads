import type { QueuedMessage } from "../../router/types";
import { splitAttachments } from "../attachmentRefs";
import styles from "./Composer.module.css";

export interface QueuedMessagesProps {
  /** The session's queued messages, owned by the session process and mirrored by the caller. */
  queued: QueuedMessage[];
  /** The user removed a queued message before it went out. */
  onCancel?: (queuedId: string) => void;
  className?: string;
}

/**
 * The user's messages waiting behind the in-flight turn, each with a remove
 * button. ConversationTab shows them above the session dock (mockup E's
 * "above" strip, next to the working line); Composer still renders them
 * itself when handed a `queued` list directly.
 */
export function QueuedMessages({ queued, onCancel, className }: QueuedMessagesProps) {
  if (queued.length === 0) return null;
  return (
    <div className={[styles.queued, className].filter(Boolean).join(" ")} data-testid="queued-messages">
      {queued.map((q) => (
        <div key={q.id} className={styles.queuedItem}>
          <span className={styles.queuedBadge}>Queued</span>
          <span className={styles.queuedText}>{queuedLabel(q.text)}</span>
          <button
            type="button"
            className={styles.queuedRemove}
            aria-label="Remove queued message"
            onClick={() => onCancel?.(q.id)}
          >
            ✕
          </button>
        </div>
      ))}
    </div>
  );
}

/** A queued message's text without the attachment paths the Router appended. */
function queuedLabel(text: string): string {
  const { body, attachments } = splitAttachments(text);
  if (attachments.length === 0) return text;
  const files = `📎 ${attachments.length} file${attachments.length === 1 ? "" : "s"}`;
  return body ? `${body} · ${files}` : files;
}
