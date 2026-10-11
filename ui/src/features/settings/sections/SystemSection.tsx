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

export { SHELLS_ANCHOR_ID } from "./ShellsSection";

export interface SystemSectionProps {
  config: UseConfig;
  /** The shells sheet, lifted so a #shells link can open it. */
  shellsOpen: boolean;
  onShellsOpenChange: (open: boolean) => void;
}

/**
 * Housekeeping: the preflight health check (shared with the wizard's
 * PreflightStep), the live dock shells, log retention and conversation import.
 */
export function SystemSection({ config, shellsOpen, onShellsOpenChange }: SystemSectionProps) {
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
        <ShellsSection open={shellsOpen} onOpenChange={onShellsOpenChange} />
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
