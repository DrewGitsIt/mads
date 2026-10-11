import { Button } from "../../../components/Button/Button";
import { NumberField } from "../../../components/NumberField/NumberField";
import { ConversationImport } from "../ConversationImport";
import { PreflightChecks } from "../PreflightChecks";
import { restartTag } from "../settingHelpers";
import { SettingRow } from "../SettingRow";
import { SettingsCard } from "../SettingsCard";
import { usePreflight } from "../state/usePreflight";
import type { UseConfig } from "../state/useConfig";
import { ShellsSection } from "./ShellsSection";
import styles from "./SystemSection.module.css";

/** Where the Shells block sits, for links that ask for it (#shells, see resolveSectionHash). */
export const SHELLS_ANCHOR_ID = "system-shells";

export interface SystemSectionProps {
  config: UseConfig;
}

/**
 * Housekeeping: the preflight health check (shared with the wizard's
 * PreflightStep), the live dock shells, log retention and conversation import.
 */
export function SystemSection({ config }: SystemSectionProps) {
  const { draft, fieldErrors, setDraft } = config;
  const preflight = usePreflight();
  if (!draft) return null;

  return (
    <div className={styles.panel}>
      <SettingsCard>
        <div className={styles.checksHeader}>
          <h3 className={styles.groupTitle}>Health check</h3>
          <Button variant="ghost" size="sm" onClick={preflight.rerun} disabled={preflight.loading}>
            Run again
          </Button>
        </div>
        <div className={styles.checks}>
          <PreflightChecks checks={preflight.checks} loading={preflight.loading} onRerun={preflight.rerun} />
        </div>
      </SettingsCard>

      <SettingsCard>
        <SettingRow
          stacked
          label="Shells"
          help="Terminals in session docks. They keep running when the dock is hidden."
        >
          <div id={SHELLS_ANCHOR_ID} className={styles.fill}>
            <ShellsSection />
          </div>
        </SettingRow>
        <SettingRow
          label="Keep logs for"
          htmlFor="log-retention-days"
          tag={restartTag("log_retention_days")}
          error={fieldErrors.log_retention_days}
        >
          <NumberField
            id="log-retention-days"
            min={1}
            unit="days"
            value={draft.log_retention_days}
            onChange={(n) => setDraft({ ...draft, log_retention_days: n })}
          />
        </SettingRow>
        <SettingRow stacked label="Import conversations">
          <ConversationImport />
        </SettingRow>
      </SettingsCard>
    </div>
  );
}
