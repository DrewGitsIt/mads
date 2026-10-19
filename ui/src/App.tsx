import { useCallback, useEffect, useMemo, useRef, useState, type ComponentType } from "react";
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
import { applyMotion } from "./features/settings/theme/applyMotion";
import { FirstRunWizard } from "./onboarding/FirstRunWizard";
import { setFlags } from "./featureFlags";
import { router } from "./router/client";
import type { Config } from "./router/types";
import { LibrarySurface } from "./library/LibrarySurface";
import { DashboardTab } from "./dashboard/DashboardTab";
import { pipelineClient } from "./pipeline/client/pipelineClient";
import type { WorkspaceTabId } from "./features/sessions/workspace/tabRegistry";
import { useEndDeletedSessionShells } from "./conversation/ShellDock/useEndDeletedSessionShells";
import { requestShellToggle } from "./conversation/shellToggleSignal";
import {
  guardLeave,
  OPEN_LIBRARY_EVENT,
  OPEN_SESSION_EVENT,
  OPEN_SETTINGS_EVENT,
  requestOpenSession,
  setRequestedLibraryShelf,
  type OpenSessionDetail,
} from "./appNavigation";
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
// Run history's "View run" opens that session's Tasks tab on the run.
function DashboardSurface() {
  return <DashboardTab onDrillBack={({ sessionId, runSeq }) => requestOpenSession(sessionId, { run: runSeq })} />;
}

const SURFACES: Partial<Record<string, ComponentType>> = {
  library: LibrarySurface,
  dashboard: DashboardSurface,
  retro: RetroSurface,
  settings: SettingsSurface,
};

export function App() {
  const [activeId, setActiveIdState] = useState("sessions");
  // Every surface switch goes through here: leaving the current surface asks
  // its leave guard first (Settings holds it while it has unsaved edits), and
  // `then` — the jump's own state — only runs once the leave goes ahead. A ref
  // so the window-event listeners below, bound once, see the live surface.
  const activeIdRef = useRef(activeId);
  const setActiveId = useCallback((id: string, then?: () => void) => {
    const go = () => {
      activeIdRef.current = id;
      setActiveIdState(id);
      then?.();
    };
    if (id === activeIdRef.current) go();
    else guardLeave(go);
  }, []);
  const [config, setConfig] = useState<Config | null>(null);
  const [configError, setConfigError] = useState(false);

  const loadConfig = useCallback(() => {
    setConfigError(false);
    router
      .getConfig()
      .then((c) => {
        applyTheme(c.theme, c.palette);
        applyMotion(c.reduce_motion);
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

  // App-level, not in a surface: a deleted session's shell must end whichever
  // surface is showing (see the hook).
  useEndDeletedSessionShells();

  const [paletteOpen, setPaletteOpen] = useState(false);
  const [jumpToSessionId, setJumpToSessionId] = useState<string>();
  const [jumpToTab, setJumpToTab] = useState<WorkspaceTabId>();
  const [jumpToRun, setJumpToRun] = useState<{ run: number | "new" }>();
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

  // Signals from deep inside a surface (the shell dock's "Manage", the Settings
  // shell inventory's session link, Settings' "Open in Library") to switch
  // surface; see appNavigation.ts.
  useEffect(() => {
    const openSettings = () => setActiveId("settings");
    const openSession = (e: Event) => {
      const { sessionId, run } = (e as CustomEvent<OpenSessionDetail>).detail;
      setActiveId("sessions", () => {
        setJumpToSessionId(sessionId);
        setJumpToTab(run === undefined ? undefined : "tasks");
        setJumpToRun(run === undefined ? undefined : { run });
      });
    };
    const openLibrary = (e: Event) => {
      const { shelf } = (e as CustomEvent<{ shelf: string }>).detail;
      setActiveId("library", () => setRequestedLibraryShelf(shelf));
    };
    window.addEventListener(OPEN_SETTINGS_EVENT, openSettings);
    window.addEventListener(OPEN_SESSION_EVENT, openSession);
    window.addEventListener(OPEN_LIBRARY_EVENT, openLibrary);
    return () => {
      window.removeEventListener(OPEN_SETTINGS_EVENT, openSettings);
      window.removeEventListener(OPEN_SESSION_EVENT, openSession);
      window.removeEventListener(OPEN_LIBRARY_EVENT, openLibrary);
    };
  }, [setActiveId]);

  const commands = useMemo(() => buildCommands(sessions, tasks), [sessions, tasks]);

  if (configError) return <ConfigGateFailure onRetry={loadConfig} />;

  // The shell renders without waiting on Config: the rail's only config-shaped
  // inputs (theme, feature flags) have safe defaults and are applied the
  // moment the response lands — the sessions list itself never needed them.
  // A hard failure (router unreachable) is still the full-screen error, and a
  // genuinely fresh install shows the wizard instead of the app.
  if (config && !config.onboarding_complete) {
    return <FirstRunWizard onComplete={() => setConfig({ ...config, onboarding_complete: true })} />;
  }

  function handleDispatch(command: Command) {
    switch (command.kind) {
      case "jump-session":
      case "open-task":
        setActiveId("sessions", () => {
          setJumpToSessionId(command.sessionId);
          setJumpToTab(undefined);
          setJumpToRun(undefined);
        });
        break;
      case "run-pipeline":
        // Plan on that session's Tasks tab: pick a template and tasks there, then Run.
        setActiveId("sessions", () => {
          setJumpToSessionId(command.sessionId);
          setJumpToTab("tasks");
          setJumpToRun({ run: "new" });
        });
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
      <IconRail items={RAIL_ITEMS} activeId={activeId} onSelect={(id) => setActiveId(id)} />
      <ErrorBoundary key={activeId}>
        {(() => {
          if (activeId === "sessions") {
            return (
              <SessionsSurface
                jumpToSessionId={jumpToSessionId}
                jumpToTab={jumpToTab}
                jumpToRun={jumpToRun}
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
