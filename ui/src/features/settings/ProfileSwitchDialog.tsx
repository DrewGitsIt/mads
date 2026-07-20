import { useEffect, useState } from "react";
import { Button } from "../../components/Button/Button";
import { Chip } from "../../components/Chip/Chip";
import type { Config, Profile } from "../../router/types";
import {
  computeCredentialDiff,
  computeProfileDiff,
  groupBySection,
  hasRestartChange,
  orderedDiffSections,
} from "./profileDiff";
import { SECTION_LABELS } from "./sections";
import styles from "./ProfileSwitchDialog.module.css";

export interface ProfileSwitchDialogProps {
  profile: Profile;
  currentConfig: Config;
  credentialNames: string[];
  activate: (id: string) => Promise<Config>;
  refetchConfig: () => Promise<void>;
  onApplied: (restartRequired: boolean) => void;
  onClose: () => void;
}

function formatValue(value: unknown): string {
  if (value === undefined || value === null || value === "") return "(none)";
  if (typeof value === "boolean") return value ? "On" : "Off";
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}

/** Grouped diff dialog shown before switching the active profile. */
export function ProfileSwitchDialog({
  profile,
  currentConfig,
  credentialNames,
  activate,
  refetchConfig,
  onApplied,
  onClose,
}: ProfileSwitchDialogProps) {
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    function handleKeyDown(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
    }
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [onClose]);

  const fieldDiff = computeProfileDiff(currentConfig, profile.snapshot);
  const grouped = groupBySection(fieldDiff);
  const sections = orderedDiffSections(grouped);
  const credentialDiff = computeCredentialDiff(
    credentialNames,
    currentConfig.credential_bindings,
    profile.snapshot.credential_bindings as string[] | undefined,
  );
  const restartRequired = hasRestartChange(fieldDiff);
  const hasChanges = fieldDiff.length > 0 || credentialDiff.length > 0;

  async function handleApply() {
    setSubmitting(true);
    try {
      await activate(profile.id);
      await refetchConfig();
      onApplied(restartRequired);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className={styles.backdrop} onMouseDown={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="profile-switch-title"
        className={styles.dialog}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div id="profile-switch-title" className={styles.title}>
          Switch to “{profile.name}”
        </div>
        {restartRequired && (
          <Chip tone="warn" className={styles.restartNotice}>
            Some changes apply on mads restart
          </Chip>
        )}
        {!hasChanges && (
          <p className={styles.empty}>No settings differ — switching only changes the active profile.</p>
        )}
        {sections.map((sectionId) => (
          <div key={sectionId} className={styles.section}>
            <div className={styles.sectionTitle}>{SECTION_LABELS[sectionId]}</div>
            <ul className={styles.fieldList}>
              {grouped[sectionId]?.map((entry) => (
                <li key={entry.field} className={styles.fieldRow}>
                  <span className={styles.fieldLabel}>{entry.label}</span>
                  <span className={styles.fieldChange}>
                    {formatValue(entry.oldValue)} → {formatValue(entry.newValue)}
                  </span>
                  {entry.restart && (
                    <Chip tone="warn" className={styles.restartChip}>
                      Restart
                    </Chip>
                  )}
                </li>
              ))}
            </ul>
          </div>
        ))}
        {credentialDiff.length > 0 && (
          <div className={styles.section}>
            <div className={styles.sectionTitle}>{SECTION_LABELS.credentials}</div>
            <ul className={styles.fieldList}>
              {credentialDiff.map((entry) => (
                <li key={entry.name} className={styles.fieldRow}>
                  <span className={styles.fieldLabel}>{entry.name}</span>
                  <span className={styles.fieldChange}>
                    {entry.wasActive ? "active" : "inactive"} → {entry.willBeActive ? "active" : "inactive"}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}
        <div className={styles.actions}>
          <Button variant="ghost" onClick={onClose} disabled={submitting}>
            Cancel
          </Button>
          <Button variant="primary" onClick={handleApply} disabled={submitting}>
            Switch profile
          </Button>
        </div>
      </div>
    </div>
  );
}
