import { useState } from "react";
import { SettingsNav, type SettingsPanelId } from "./nav/SettingsNav";
import { useConfig } from "./state/useConfig";
import { AppearancePanel } from "./panels/AppearancePanel";
import { GeneralPanel } from "./panels/GeneralPanel";
import { ProfilesPanel } from "./panels/ProfilesPanel";
import { ProvidersPanel } from "./panels/ProvidersPanel";
import { CredentialsPanel } from "./panels/CredentialsPanel";
import { PipelinePanel } from "./panels/PipelinePanel";
import { AdvancedPanel } from "./panels/AdvancedPanel";
import { UpdatesAboutPanel } from "./panels/UpdatesAboutPanel";
import styles from "./SettingsSurface.module.css";

/**
 * Top-level Settings surface (registered SURFACES.settings). Owns the single
 * useConfig instance shared by every config-backed panel — panels never
 * fetch/hold their own copy of Config (HANDOFF §4).
 */
export function SettingsSurface() {
  const [activeId, setActiveId] = useState<SettingsPanelId>("appearance");
  const config = useConfig();

  if (config.loading || !config.draft) {
    return <div className={styles.loading}>Loading settings…</div>;
  }

  return (
    <div className={styles.surface}>
      <SettingsNav activeId={activeId} onSelect={setActiveId} />
      <div className={styles.body}>
        {activeId === "appearance" && <AppearancePanel config={config} />}
        {activeId === "general" && <GeneralPanel config={config} />}
        {activeId === "profiles" && <ProfilesPanel config={config} />}
        {activeId === "providers" && <ProvidersPanel />}
        {activeId === "credentials" && <CredentialsPanel />}
        {activeId === "pipeline" && <PipelinePanel config={config} />}
        {activeId === "advanced" && <AdvancedPanel config={config} />}
        {activeId === "updates" && <UpdatesAboutPanel />}
      </div>
    </div>
  );
}
