import { useState } from "react";
import { Panel, PanelBody, PanelFooter } from "../../../components/Panel/Panel";
import type { SessionActivity } from "../../../router/types";
import { sleepState, wakeState, deleteState } from "./sessionLifecycle";
import styles from "./SessionActionsMenu.module.css";

/**
 * Where the session's shell stands, for the toggle's label: no live shell
 * ("none": the toggle starts one), alive but its dock hidden ("hidden"), or
 * its dock showing ("visible").
 */
export type ShellMenuState = "none" | "hidden" | "visible";

export function shellMenuState(dockOpen: boolean, shellAlive: boolean): ShellMenuState {
  if (dockOpen) return "visible";
  return shellAlive ? "hidden" : "none";
}

export interface SessionActionsMenuProps {
  sessionId: string;
  activity: SessionActivity;
  /** Sleep this session — the caller routes it through useSessionSleep, which may ask about unlanded commits first. */
  onSleep: () => void;
  /** Pre-warm a sleeping session. Never prompts. */
  onWake: () => void;
  onDeleteRequest: () => void;
  /** Linked sessions (flag-gated): only passed for a row that is an actual link subject. */
  onUnlinkRequest?: () => void;
  /**
   * Conversation-tab tools folded into this one menu so the workspace has a
   * single kebab (not one here + one in the conversation itself). Passed only
   * by SessionWorkspace; the rail never sends them, so its cards stay
   * lifecycle-only.
   */
  onToggleShell?: () => void;
  /** Labels the shell item; omitted reads as no live shell. */
  shellState?: ShellMenuState;
  onRestartAgent?: () => void;
  /** True while a bounce triggered from onRestartAgent is in flight — disables the item so a slow bounce can't be double-fired. */
  restartingAgent?: boolean;
  className?: string;
}

/** Shared kebab/rail-row popover content: all three lifecycle actions always render, unavailable ones disabled with an inline reason. */
export function SessionActionsMenu({
  sessionId,
  activity,
  onSleep,
  onWake,
  onDeleteRequest,
  onUnlinkRequest,
  onToggleShell,
  shellState = "none",
  onRestartAgent,
  restartingAgent,
  className,
}: SessionActionsMenuProps) {
  const sleep = sleepState();
  const wake = wakeState(activity);
  const del = deleteState();
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
                    {shellState === "visible" ? "Hide shell" : shellState === "hidden" ? "Show shell" : "Open shell"}
                    {shellState === "hidden" && (
                      // A hidden shell is still running; the dot is the reminder.
                      <span className={styles.liveDot} role="img" aria-label="shell is running" />
                    )}
                  </button>
                </li>
              )}
              {onRestartAgent && (
                <li>
                  <button
                    type="button"
                    className={styles.menuItem}
                    disabled={restartingAgent}
                    onClick={onRestartAgent}
                  >
                    {restartingAgent ? "Restarting Agent…" : "Restart Agent"}
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
              disabled={sleep.disabled}
              onClick={onSleep}
            >
              Sleep now
              {sleep.reason && <span className={styles.reason}>{sleep.reason}</span>}
            </button>
          </li>
          <li>
            <button
              type="button"
              className={styles.menuItem}
              disabled={wake.disabled}
              onClick={onWake}
            >
              Wake now
              {wake.reason && <span className={styles.reason}>{wake.reason}</span>}
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
