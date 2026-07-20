import { Button } from "../../../components/Button/Button";
import { useUpdateCheck } from "../state/useUpdateCheck";
import styles from "./UpdatesAboutSection.module.css";

/** Two-column version/notes + About copy. Update checking only fires when the user clicks the button — never on mount, never on a timer (§14.3, W7.5). */
export function UpdatesAboutSection() {
  const { currentVersion, result, checking, check } = useUpdateCheck();

  return (
    <div className={styles.panel}>
      <div className={styles.column}>
        <h3 className={styles.heading}>Updates</h3>
        <div className={styles.version}>Version {currentVersion || "—"}</div>
        <p className={styles.notes}>{updateStatusCopy(result)}</p>
        <Button variant="secondary" disabled={checking} onClick={check}>
          {checking ? "Checking…" : "Check for updates"}
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

function updateStatusCopy(result: ReturnType<typeof useUpdateCheck>["result"]): string {
  if (!result) return "Check for updates to see if a newer build is available.";
  if (result.detail) return result.detail;
  if (result.update_available) {
    return result.notes
      ? `Update available: v${result.latest_version}. ${result.notes}`
      : `Update available: v${result.latest_version}.`;
  }
  return "You’re on the latest build.";
}
