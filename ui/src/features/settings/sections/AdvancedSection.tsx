import { useState } from "react";
import { setFlags } from "../../../featureFlags";
import type { UseConfig } from "../state/useConfig";
import type { FeatureFlags } from "../../../router/types";
import styles from "./AdvancedSection.module.css";

const TOGGLES: Array<{ key: keyof FeatureFlags; label: string; description: string }> = [
  {
    key: "multi_repo",
    label: "Multi-repo workspaces",
    description: "Allow a workspace to span more than one repository.",
  },
  {
    key: "linked_sessions",
    label: "Linked sessions",
    description: "Enable leader/monitor session linking: rail multi-select, link chooser, and attributed inject/digest rendering.",
  },
  {
    key: "remote_registry",
    label: "Remote registry",
    description: "Browse and install agents/capabilities/templates from a remote mads registry.",
  },
];

export interface AdvancedSectionProps {
  config: UseConfig;
}

/**
 * Three feature flag toggles: multi-repo, linked-sessions, remote-registry
 * (HANDOFF §9). feature_flags applies live — it's not in
 * RESTART_REQUIRED_FIELDS (pkg/contracts/config.go's canonical set), so no
 * restart badge here.
 */
export function AdvancedSection({ config }: AdvancedSectionProps) {
  const { draft, setDraft, save } = config;
  const [saving, setSaving] = useState<keyof FeatureFlags | null>(null);

  if (!draft) return null;

  const toggle = async (key: keyof FeatureFlags) => {
    const nextFlags = { ...draft.feature_flags, [key]: !draft.feature_flags[key] };
    const next = { ...draft, feature_flags: nextFlags };
    setDraft(next);
    setSaving(key);
    const ok = await save(next);
    if (ok) {
      setFlags({
        multiRepo: nextFlags.multi_repo,
        linkedSessions: nextFlags.linked_sessions,
        remoteRegistry: nextFlags.remote_registry,
      });
    }
    setSaving(null);
  };

  return (
    <div className={styles.panel}>
      {TOGGLES.map((item) => (
        <div key={item.key} className={styles.row}>
          <label className={styles.toggleLabel}>
            <input
              type="checkbox"
              checked={draft.feature_flags[item.key]}
              disabled={saving === item.key}
              onChange={() => void toggle(item.key)}
            />
            {item.label}
          </label>
          <div className={styles.description}>{item.description}</div>
        </div>
      ))}
    </div>
  );
}
