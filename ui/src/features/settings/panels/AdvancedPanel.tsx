import { useState } from "react";
import { RestartBadge } from "../RestartBadge";
import { setFlags } from "../../../featureFlags";
import type { UseConfig } from "../state/useConfig";
import type { FeatureFlags } from "../../../router/types";
import styles from "./AdvancedPanel.module.css";

const TOGGLES: Array<{ key: keyof FeatureFlags; label: string; description: string }> = [
  {
    key: "multi_repo",
    label: "Multi-repo workspaces",
    description: "Allow a workspace to span more than one repository.",
  },
  {
    key: "linked_sessions",
    label: "Linked sessions",
    description: "Enable leader/monitor session linking (toggle only — no UI yet).",
  },
  {
    key: "remote_registry",
    label: "Remote registry",
    description: "Browse and connect to a remote mads registry (toggle only — no UI yet).",
  },
];

export interface AdvancedPanelProps {
  config: UseConfig;
}

/** Three feature flag toggles: multi-repo, linked-sessions, remote-registry (HANDOFF §9). */
export function AdvancedPanel({ config }: AdvancedPanelProps) {
  const { draft, dirtyRestartFields, setDraft, save } = config;
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

  const restartVisible = dirtyRestartFields.includes("feature_flags");

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
      <RestartBadge visible={restartVisible} />
    </div>
  );
}
