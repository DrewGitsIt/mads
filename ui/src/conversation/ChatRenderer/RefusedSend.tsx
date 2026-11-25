import { useState } from "react";
import { Button } from "../../components/Button/Button";
import type { RefusedSend as Refused } from "./sendRefusal";
import chat from "./ChatRenderer.module.css";
import delivery from "./SendDelivery.module.css";
import styles from "./RefusedSend.module.css";

export interface RefusedSendProps {
  refused: Refused;
  /** The session's host: "pop-os". */
  host: string;
  /** This device's name, as the host knows it. */
  device: string;
  onRetry: () => void;
  retrying?: boolean;
}

/**
 * A message the session's host refused for this device's grant: the bubble
 * stays, outlined, with "Not sent", the reason in plain words, Why? (what's
 * set where, and how to send it) and Retry.
 */
export function RefusedSend({ refused, host, device, onRetry, retrying }: RefusedSendProps) {
  const [open, setOpen] = useState(false);
  const line = {
    c1: `This device can't message Full access sessions on ${host}.`,
    view: `This device may only view mads on ${host}.`,
    none: `${host} doesn't let this device use mads there.`,
  }[refused.kind];
  return (
    <div className={chat.user} data-undelivered="" data-refused-send={refused.kind}>
      <div className={chat.sent}>
        <div className={chat.prose}>{refused.text}</div>
        <div className={delivery.delivery}>
          <div className={[delivery.failed, styles.line].join(" ")} role="status">
            <span className={styles.reason}>
              <span className={delivery.failedLabel}>Not sent</span> {line}
            </span>
            <span className={styles.actions}>
              <button type="button" className={delivery.attempts} aria-expanded={open} onClick={() => setOpen((o) => !o)}>
                {open ? "Hide" : "Why?"}
              </button>
              <Button variant="ghost" size="sm" onClick={onRetry} disabled={retrying}>
                Retry
              </Button>
            </span>
          </div>
          {open && (
            <div className={styles.detail}>
              <h4>Why</h4>
              {refused.kind === "c1" ? (
                <>
                  <ul>
                    <li>
                      This session runs at <b>Full access</b> on {host}.
                    </li>
                    <li>
                      {device} may <b>chat with</b> mads there, and {host} refuses its messages to Full access sessions.
                    </li>
                    <li>
                      Set on <b>{host}</b>: <span className={styles.where}>Settings → Paired devices → {device}</span>
                    </li>
                  </ul>
                  <h4>To send it</h4>
                  <ul>
                    <li>
                      On {host}, allow {device}'s messages to Full access sessions, or
                    </li>
                    <li>lower this session's trust from a device that may control mads there, or</li>
                    <li>
                      let {device} control mads on {host}.
                    </li>
                  </ul>
                </>
              ) : (
                <ul>
                  <li>
                    {device} may <b>{refused.kind === "view" ? "view" : "not use"}</b> mads on {host}. Sending needs chat with.
                  </li>
                  <li>
                    Set on <b>{host}</b>: <span className={styles.where}>Settings → Paired devices → {device}</span>
                  </li>
                </ul>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
