import { useCallback, useEffect, useMemo, useState, type ComponentType } from "react";
import { ErrorBoundary } from "./components/ErrorBoundary/ErrorBoundary";
import { IconRail, type IconRailItem } from "./components/IconRail/IconRail";
import { ConfigGateFailure } from "./ConfigGateFailure";
import { CommandPalette } from "./features/command-palette/CommandPalette";
import { buildCommands, type Command } from "./features/command-palette/commandSource";
import { useAttentionBadge } from "./features/command-palette/state/useAttentionBadge";
import { useCommandPaletteData } from "./features/command-palette/state/useCommandPaletteData";
import { RetroSurface } from "./features/retro/RetroSurface";
import { SessionsSurface } from "./features/sessions/SessionsSurface";
import { SettingsSurface } from "./features/settings/SettingsSurface";
import { applyTheme } from "./features/settings/theme/applyTheme";
import { FirstRunWizard } from "./onboarding/FirstRunWizard";
import { setFlags } from "./featureFlags";
import { router } from "./router/client";
import type { Config } from "./router/types";
import { LibrarySurface } from "./library/LibrarySurface";
import { DashboardTab } from "./dashboard/DashboardTab";
import { pipelineClient } from "./pipeline/client/pipelineClient";
import type { WorkspaceTabId } from "./features/sessions/workspace/tabRegistry";
import { requestShellToggle } from "./conversation/shellToggleSignal";
import { setPlatform } from "./platform/shortcut";
import styles from "./App.module.css";

const RAIL_ITEMS: IconRailItem[] = [
  { id: "sessions", label: "Sessions", icon: "S" },
  { id: "retro", label: "Retro", icon: "R" },
  { id: "library", label: "Library", icon: "L" },
  { id: "settings", label: "Settings", icon: "⚙" },
  { id: "dashboard", label: "Dashboard", icon: "D" },
];

// "sessions" is special-cased in the render switch below (needs
// jumpToSessionId/jumpToTab/pipelineClient props SURFACES' ComponentType
// signature can't carry), so it's deliberately absent here.
const SURFACES: Partial<Record<string, ComponentType>> = {
  library: LibrarySurface,
  dashboard: DashboardTab,
  retro: RetroSurface,
  settings: SettingsSurface,
};

export function App() {
  const [activeId, setActiveId] = useState("sessions");
  const [config, setConfig] = useState<Config | null>(null);
  const [configError, setConfigError] = useState(false);

  const loadConfig = useCallback(() => {
    setConfigError(false);
    router
      .getConfig()
      .then((c) => {
        applyTheme(c.theme, c.palette);
        setPlatform(c.platform);
        setFlags({
          multiRepo: c.feature_flags.multi_repo,
          linkedSessions: c.feature_flags.linked_sessions,
          remoteRegistry: c.feature_flags.remote_registry,
        });
        setConfig(c);
      })
      .catch(() => setConfigError(true));
  }, []);

  useEffect(() => {
    loadConfig();
  }, [loadConfig]);

  const [paletteOpen, setPaletteOpen] = useState(false);
  const [jumpToSessionId, setJumpToSessionId] = useState<string>();
  const [jumpToTab, setJumpToTab] = useState<WorkspaceTabId>();
  const { sessions, tasks, openTaskCounts } = useCommandPaletteData();

  useAttentionBadge(sessions, openTaskCounts);

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      // ⌘/Ctrl-⇧-P, not ⌘K: the sessions rail's omni search owns ⌘K (#2b),
      // and it is the surface this chord was mostly used to reach.
      if ((e.metaKey || e.ctrlKey) && e.shiftKey && e.key.toLowerCase() === "p") {
        e.preventDefault();
        setPaletteOpen((v) => !v);
      } else if (e.ctrlKey && e.key === "`") {
        e.preventDefault();
        requestShellToggle();
      }
    };
    document.addEventListener("keydown", handler);
    return () => document.removeEventListener("keydown", handler);
  }, []);

  const commands = useMemo(() => buildCommands(sessions, tasks), [sessions, tasks]);

  if (configError) return <ConfigGateFailure onRetry={loadConfig} />;

  if (!config) return null;

  if (!config.onboarding_complete) {
    return <FirstRunWizard onComplete={() => setConfig({ ...config, onboarding_complete: true })} />;
  }

  function handleDispatch(command: Command) {
    switch (command.kind) {
      case "jump-session":
      case "open-task":
        setActiveId("sessions");
        setJumpToSessionId(command.sessionId);
        setJumpToTab(undefined);
        break;
      case "start-pipeline":
        if (command.sessionId) {
          void pipelineClient.startPipeline(command.sessionId, "default-implementation");
          setActiveId("sessions");
          setJumpToSessionId(command.sessionId);
          setJumpToTab("pipeline");
        }
        break;
      case "new-session":
        // Real create flow already lives in SessionsSurface; this just navigates there.
        setActiveId("sessions");
        break;
      case "run-retro":
        setActiveId("retro");
        break;
      case "toggle-shell":
        requestShellToggle();
        break;
      case "open-settings":
        break; // no-op: no settings surface exists yet
    }
  }

  return (
    <div className={styles.shell}>
      <IconRail items={RAIL_ITEMS} activeId={activeId} onSelect={setActiveId} />
      <ErrorBoundary key={activeId}>
        {(() => {
          if (activeId === "sessions") {
            return (
              <SessionsSurface
                jumpToSessionId={jumpToSessionId}
                jumpToTab={jumpToTab}
                pipelineClient={pipelineClient}
              />
            );
          }
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
      </ErrorBoundary>
      <CommandPalette
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
        commands={commands}
        onDispatch={handleDispatch}
      />
    </div>
  );
}
