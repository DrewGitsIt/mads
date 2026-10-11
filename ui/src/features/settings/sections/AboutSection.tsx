import { Button } from "../../../components/Button/Button";
import { SettingRow } from "../SettingRow";
import { SettingsCard } from "../SettingsCard";
import { useUpdateCheck } from "../state/useUpdateCheck";
import styles from "./AboutSection.module.css";

/**
 * Version + Check for updates, then what mads is. Update checking only fires
 * when the user clicks the button — never on mount, never on a timer (§14.3,
 * W7.5). Fuller About content (the sessions → pipelines → synthesis loop,
 * links, where data lives) comes in a later pass.
 */
export function AboutSection() {
  const { currentVersion, result, checking, check } = useUpdateCheck();

  return (
    <div className={styles.panel}>
      <SettingsCard>
        <SettingRow label="Version" help={`${currentVersion || "—"} · ${updateStatusCopy(result)}`}>
          <Button variant="secondary" size="sm" disabled={checking} onClick={check}>
            {checking ? "Checking…" : "Check for updates"}
          </Button>
        </SettingRow>
      </SettingsCard>
      <SettingsCard>
        <p className={styles.about}>
          mads orchestrates multi-agent development sessions, synthesizing retros and driving follow-up work across
          your workspaces.
        </p>
      </SettingsCard>
    </div>
  );
}

function updateStatusCopy(result: ReturnType<typeof useUpdateCheck>["result"]): string {
  if (!result) return "Check to see if a newer build is available.";
  if (result.detail) return result.detail;
  if (result.update_available) {
    return result.notes
      ? `Update available: v${result.latest_version}. ${result.notes}`
      : `Update available: v${result.latest_version}.`;
  }
  return "You’re on the latest build.";
}
