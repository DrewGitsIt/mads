import { useState } from "react";
import { Button } from "../../components/Button/Button";
import type { Delivery } from "./failedSends";
import { formatMessageTime, parseMessageTime } from "./messageTime";
import styles from "./SendDelivery.module.css";

/**
 * Under a user message that failed (the harness answered with an error, or
 * the send never reached the agent) or that took more than one try (see
 * collapseFailedSends): "Not delivered" with the reason and,
 * on the latest message only, Retry; then a quiet count of earlier failed
 * attempts that opens to list each one's reason and time.
 */
export function SendDelivery({
  delivery,
  onRetry,
}: {
  delivery: Delivery;
  /** Present only when this message can be resent now. */
  onRetry?: () => void;
}) {
  const [open, setOpen] = useState(false);
  const { error, attempts } = delivery;
  const n = attempts.length;
  return (
    <div className={styles.delivery} data-find-skip>
      {error && (
        <div className={styles.failed} role="status">
          <span className={styles.failedLabel}>Not delivered</span>
          <span className={styles.reason} title={error}>
            {error}
          </span>
          {onRetry && (
            <Button variant="ghost" size="sm" className={styles.retry} onClick={onRetry}>
              Retry
            </Button>
          )}
        </div>
      )}
      {n > 0 && (
        <>
          <button
            type="button"
            className={styles.attempts}
            aria-expanded={open}
            onClick={() => setOpen((o) => !o)}
          >
            {n === 1 ? "1 failed attempt" : `${n} failed attempts`}
          </button>
          {open && (
            <ol className={styles.attemptList}>
              {attempts.map((a, i) => {
                const at = parseMessageTime(a.timestamp);
                return (
                  <li key={i}>
                    {at && (
                      <time dateTime={at.toISOString()} title={at.toLocaleString()}>
                        {formatMessageTime(at)}
                      </time>
                    )}
                    <span className={styles.reason} title={a.error}>
                      {a.error}
                    </span>
                  </li>
                );
              })}
            </ol>
          )}
        </>
      )}
    </div>
  );
}
