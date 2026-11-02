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
 *
 * The latest send, refused by the harness for auth while the session's login
 * is still signed out, reads "Signed out of {provider}" instead (the reason moves to
 * the tooltip) and offers Sign in beside Retry. Once the login is signed in
 * again it reads "Not delivered" like any other failure.
 */
export function SendDelivery({
  delivery,
  onRetry,
  signIn,
}: {
  delivery: Delivery;
  /** Present only when this message can be resent now. */
  onRetry?: () => void;
  /** Present only while the session's login is signed out: its provider's display name and the way back in. */
  signIn?: { provider: string; onSignIn: () => void };
}) {
  const [open, setOpen] = useState(false);
  const { error, attempts } = delivery;
  const n = attempts.length;
  const signedOut = signIn && delivery.errorKind === "auth" ? signIn : undefined;
  return (
    <div className={styles.delivery} data-find-skip>
      {error && (
        <div className={styles.failed} role="status">
          {signedOut ? (
            <>
              <span className={styles.failedLabel} title={error}>
                Signed out of {signedOut.provider}
              </span>
              <Button variant="secondary" size="sm" className={styles.retry} onClick={signedOut.onSignIn}>
                Sign in
              </Button>
            </>
          ) : (
            <>
              <span className={styles.failedLabel}>Not delivered</span>
              <span className={styles.reason} title={error}>
                {error}
              </span>
            </>
          )}
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
                    {/* "failed at": the message's own hover stamp sits on this row too. */}
                    {at && (
                      <time dateTime={at.toISOString()} title={at.toLocaleString()}>
                        failed at {formatMessageTime(at)}
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
