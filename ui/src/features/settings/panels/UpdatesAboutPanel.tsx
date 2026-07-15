import { Button } from "../../../components/Button/Button";
import styles from "./UpdatesAboutPanel.module.css";

const VERSION = "0.1.0-dev";

/** Two-column version/notes + About copy. Real update checking is W7.5 — this pass ships static content and a disabled button (HANDOFF §11). */
export function UpdatesAboutPanel() {
  return (
    <div className={styles.panel}>
      <div className={styles.column}>
        <h3 className={styles.heading}>Updates</h3>
        <div className={styles.version}>Version {VERSION}</div>
        <p className={styles.notes}>
          You&rsquo;re on the latest build. Automatic update checking lands in a future release.
        </p>
        <Button variant="secondary" disabled title="Update checking is not available yet">
          Check for updates
        </Button>
      </div>
      <div className={styles.column}>
        <h3 className={styles.heading}>About</h3>
        <p className={styles.about}>
          mads orchestrates multi-agent development sessions, synthesizing retros and driving
          follow-up work across your workspaces.
        </p>
      </div>
    </div>
  );
}
