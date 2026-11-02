import type { Attribution, QueuedMessage } from "../../router/types";
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
 * The messages waiting behind the in-flight turn. The user's own each have a
 * remove button; a linked session's are labelled with their sender and have
 * none (the sender was already told it was delivered). ConversationTab shows
 * them above the session dock (mockup E's "above" strip, next to the working
 * line); Composer still renders them itself when handed a `queued` list
 * directly.
 */
export function QueuedMessages({
  queued,
  onCancel,
  className,
}: QueuedMessagesProps) {
  if (queued.length === 0) return null;
  return (
    <div
      className={[styles.queued, className].filter(Boolean).join(" ")}
      data-testid="queued-messages"
    >
      {groupDigests(queued).map(({ head, count }) =>
        head.attribution ? (
          <div key={head.id} className={styles.queuedItem} data-injected>
            <span className={styles.queuedBadge}>Queued</span>
            <span className={styles.queuedFrom}>{injectedLabel(head.attribution, count)}</span>
            {count === 1 && <span className={styles.queuedText}>{head.text}</span>}
          </div>
        ) : (
          <div key={head.id} className={styles.queuedItem}>
            <span className={styles.queuedBadge}>Queued</span>
            <span className={styles.queuedText}>{queuedLabel(head.text)}</span>
            <button
              type="button"
              className={styles.queuedRemove}
              aria-label="Remove queued message"
              onClick={() => onCancel?.(head.id)}
            >
              ✕
            </button>
          </div>
        ),
      )}
    </div>
  );
}

/** Folds each run of consecutive digests from one link into one row — the
 * session runner delivers such a run as a single turn (drainNextLocked). */
function groupDigests(queued: QueuedMessage[]): { head: QueuedMessage; count: number }[] {
  const rows: { head: QueuedMessage; count: number }[] = [];
  for (const q of queued) {
    const last = rows[rows.length - 1];
    const a = q.attribution;
    if (last && a?.digest_attributed && last.head.attribution?.digest_attributed
      && last.head.attribution.link_id === a.link_id) {
      last.count++;
    } else {
      rows.push({ head: q, count: 1 });
    }
  }
  return rows;
}

/** Who a queued injection is from, worded like its delivered chip. */
function injectedLabel(a: Attribution, count: number): string {
  const from = a.source_name || "mads";
  if (a.digest_attributed || a.kind === "digest") {
    return count > 1 ? `${count} digests from ${from}` : `Digest from ${from}`;
  }
  if (a.kind === "signal") return `Signal from ${from}`;
  if (a.kind === "notice") return `Notice from ${from}`;
  return `⇄ from ${a.relation ?? "a linked session"} · ${from}`;
}

/** A queued message's text without the attachment paths the Router appended. */
function queuedLabel(text: string): string {
  const { body, attachments } = splitAttachments(text);
  if (attachments.length === 0) return text;
  const files = `📎 ${attachments.length} file${attachments.length === 1 ? "" : "s"}`;
  return body ? `${body} · ${files}` : files;
}
