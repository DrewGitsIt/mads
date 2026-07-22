import { useState } from "react";
import { Panel, PanelBody, PanelFooter } from "../../../components/Panel/Panel";
import type { SessionActivity } from "../../../router/types";
import { closeState, resumeState, deleteState } from "./sessionLifecycle";
import styles from "./SessionActionsMenu.module.css";

export interface SessionActionsMenuProps {
  sessionId: string;
  activity: SessionActivity;
  onClose: () => void;
  onResume: () => void;
  onDeleteRequest: () => void;
  /** Linked sessions (flag-gated): only passed for a row that is an actual link subject. */
  onUnlinkRequest?: () => void;
  /**
   * Conversation-tab tools folded into this one menu so the workspace has a
   * single kebab (not one here + one in the ConversationHeader). Passed only
   * by SessionWorkspace; the rail never sends them, so its cards stay
   * lifecycle-only.
   */
  onToggleShell?: () => void;
  onRestartAgent?: () => void;
  className?: string;
}

/** Shared kebab/rail-row popover content: all three lifecycle actions always render, unavailable ones disabled with an inline reason. */
export function SessionActionsMenu({
  sessionId,
  activity,
  onClose,
  onResume,
  onDeleteRequest,
  onUnlinkRequest,
  onToggleShell,
  onRestartAgent,
  className,
}: SessionActionsMenuProps) {
  const close = closeState(activity);
  const resume = resumeState(activity);
  const del = deleteState(activity);
  const [copied, setCopied] = useState(false);

  function copySessionId() {
    void navigator.clipboard.writeText(sessionId).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  }

  return (
    <Panel className={className}>
      <PanelBody>
        <ul className={styles.menuList}>
          {(onToggleShell || onRestartAgent) && (
            <>
              {onToggleShell && (
                <li>
                  <button type="button" className={styles.menuItem} onClick={onToggleShell}>
                    Shell
                  </button>
                </li>
              )}
              {onRestartAgent && (
                <li>
                  <button type="button" className={styles.menuItem} onClick={onRestartAgent}>
                    Restart Agent
                  </button>
                </li>
              )}
              <li aria-hidden="true" className={styles.divider} />
            </>
          )}
          <li>
            <button
              type="button"
              className={styles.menuItem}
              disabled={close.disabled}
              onClick={onClose}
            >
              Close
              {close.reason && <span className={styles.reason}>{close.reason}</span>}
            </button>
          </li>
          <li>
            <button
              type="button"
              className={styles.menuItem}
              disabled={resume.disabled}
              onClick={onResume}
            >
              Resume
              {resume.reason && <span className={styles.reason}>{resume.reason}</span>}
            </button>
          </li>
          <li>
            <button
              type="button"
              className={styles.menuItem}
              disabled={del.disabled}
              onClick={onDeleteRequest}
            >
              Delete
              {del.reason && <span className={styles.reason}>{del.reason}</span>}
            </button>
          </li>
          {onUnlinkRequest && (
            <li>
              <button type="button" className={styles.menuItem} onClick={onUnlinkRequest}>
                Unlink
              </button>
            </li>
          )}
        </ul>
      </PanelBody>
      <PanelFooter>
        <button
          type="button"
          className={styles.idRow}
          onClick={copySessionId}
        >
          {copied ? "Copied" : sessionId}
        </button>
      </PanelFooter>
    </Panel>
  );
}
