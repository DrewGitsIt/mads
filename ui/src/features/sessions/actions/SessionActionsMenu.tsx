import { Panel, PanelBody } from "../../../components/Panel/Panel";
import type { SessionActivity } from "../../../router/types";
import { closeState, resumeState, deleteState } from "./sessionLifecycle";
import styles from "./SessionActionsMenu.module.css";

export interface SessionActionsMenuProps {
  activity: SessionActivity;
  onClose: () => void;
  onResume: () => void;
  onDeleteRequest: () => void;
  /** Linked sessions (flag-gated): only passed for a row that is an actual link subject. */
  onUnlinkRequest?: () => void;
  className?: string;
}

/** Shared kebab/rail-row popover content: all three lifecycle actions always render, unavailable ones disabled with an inline reason. */
export function SessionActionsMenu({
  activity,
  onClose,
  onResume,
  onDeleteRequest,
  onUnlinkRequest,
  className,
}: SessionActionsMenuProps) {
  const close = closeState(activity);
  const resume = resumeState(activity);
  const del = deleteState(activity);
  return (
    <Panel className={className}>
      <PanelBody>
        <ul className={styles.menuList}>
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
    </Panel>
  );
}
