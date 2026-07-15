import styles from "./SettingsNav.module.css";

export type SettingsPanelId =
  | "appearance"
  | "general"
  | "profiles"
  | "providers"
  | "credentials"
  | "pipeline"
  | "advanced"
  | "updates";

export const SETTINGS_NAV_ITEMS: Array<{ id: SettingsPanelId; label: string }> = [
  { id: "appearance", label: "Appearance" },
  { id: "general", label: "General" },
  { id: "profiles", label: "Profiles" },
  { id: "providers", label: "Providers" },
  { id: "credentials", label: "Credentials" },
  { id: "pipeline", label: "Pipeline" },
  { id: "advanced", label: "Advanced" },
  { id: "updates", label: "Updates & About" },
];

export interface SettingsNavProps {
  activeId: SettingsPanelId;
  onSelect: (id: SettingsPanelId) => void;
}

/** Left-nav for the Settings surface, MADS-UX §3.7's exact section order. */
export function SettingsNav({ activeId, onSelect }: SettingsNavProps) {
  return (
    <nav className={styles.nav} aria-label="Settings sections">
      {SETTINGS_NAV_ITEMS.map((item) => (
        <button
          key={item.id}
          type="button"
          className={styles.item}
          aria-current={item.id === activeId ? "page" : undefined}
          onClick={() => onSelect(item.id)}
        >
          {item.label}
        </button>
      ))}
    </nav>
  );
}
