import { useEffect, useState, type ComponentType } from "react";
import { IconRail, type IconRailItem } from "./components/IconRail/IconRail";
import { RetroSurface } from "./features/retro/RetroSurface";
import { SessionsSurface } from "./features/sessions/SessionsSurface";
import { SettingsSurface } from "./features/settings/SettingsSurface";
import { applyTheme } from "./features/settings/theme/applyTheme";
import { FirstRunWizard } from "./onboarding/FirstRunWizard";
import { setFlags } from "./featureFlags";
import { routerGet } from "./router/client";
import type { Config } from "./router/types";
import styles from "./App.module.css";

const RAIL_ITEMS: IconRailItem[] = [
  { id: "sessions", label: "Sessions", icon: "S" },
  { id: "retro", label: "Retro", icon: "R" },
  { id: "library", label: "Library", icon: "L" },
  { id: "settings", label: "Settings", icon: "⚙" },
];

const SURFACES: Partial<Record<string, ComponentType>> = {
  sessions: SessionsSurface,
  retro: RetroSurface,
  settings: SettingsSurface,
};

export function App() {
  const [activeId, setActiveId] = useState("sessions");
  const [config, setConfig] = useState<Config | null>(null);

  useEffect(() => {
    routerGet<Config>("/config").then((c) => {
      applyTheme(c.theme, c.palette);
      setFlags({
        multiRepo: c.feature_flags.multi_repo,
        linkedSessions: c.feature_flags.linked_sessions,
        remoteRegistry: c.feature_flags.remote_registry,
      });
      setConfig(c);
    });
  }, []);

  if (!config) return null;

  if (!config.onboarding_complete) {
    return <FirstRunWizard onComplete={() => setConfig({ ...config, onboarding_complete: true })} />;
  }

  return (
    <div className={styles.shell}>
      <IconRail items={RAIL_ITEMS} activeId={activeId} onSelect={setActiveId} />
      {(() => {
        const Surface = SURFACES[activeId];
        return Surface ? (
          <Surface />
        ) : (
          <div className={styles.empty}>
            <div className={styles.emptyInner}>
              <div className={styles.emptyTitle}>No surfaces wired up yet</div>
              <div className={styles.emptyBody}>
                This is the app shell scaffold — sessions, retro, and library views
                land in later work items. Run <code>npm run gallery</code> to
                review the component kit this shell is built from.
              </div>
            </div>
          </div>
        );
      })()}
    </div>
  );
}
