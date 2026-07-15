import { useEffect, useMemo, useState, type ComponentType } from "react";
import { IconRail, type IconRailItem } from "./components/IconRail/IconRail";
import { CommandPalette } from "./features/command-palette/CommandPalette";
import { buildCommands, type Command } from "./features/command-palette/commandSource";
import { useAttentionBadge } from "./features/command-palette/state/useAttentionBadge";
import { useCommandPaletteData } from "./features/command-palette/state/useCommandPaletteData";
import { RetroSurface } from "./features/retro/RetroSurface";
import { SessionsSurface } from "./features/sessions/SessionsSurface";
import { LibrarySurface } from "./library/LibrarySurface";
import { DashboardTab } from "./dashboard/DashboardTab";
import { FetchPipelineClient } from "./pipeline/client/pipelineClient";
import styles from "./App.module.css";

const RAIL_ITEMS: IconRailItem[] = [
  { id: "sessions", label: "Sessions", icon: "S" },
  { id: "retro", label: "Retro", icon: "R" },
  { id: "library", label: "Library", icon: "L" },
  { id: "dashboard", label: "Dashboard", icon: "D" },
];

const SURFACES: Partial<Record<string, ComponentType>> = {
  sessions: SessionsSurface,
  library: LibrarySurface,
  dashboard: DashboardTab,
  retro: RetroSurface,
};

// Stateless — safe as a module-level singleton rather than re-created per render.
const pipelineClient = new FetchPipelineClient();

export function App() {
  const [activeId, setActiveId] = useState("sessions");
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [jumpToSessionId, setJumpToSessionId] = useState<string>();
  const { sessions, tasks, openTaskCounts } = useCommandPaletteData();

  useAttentionBadge(sessions, openTaskCounts);

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "k") {
        e.preventDefault();
        setPaletteOpen((v) => !v);
      }
    };
    document.addEventListener("keydown", handler);
    return () => document.removeEventListener("keydown", handler);
  }, []);

  const commands = useMemo(() => buildCommands(sessions, tasks), [sessions, tasks]);

  function handleDispatch(command: Command) {
    switch (command.kind) {
      case "jump-session":
      case "open-task":
        setActiveId("sessions");
        setJumpToSessionId(command.sessionId);
        break;
      case "start-pipeline":
        if (command.sessionId) void pipelineClient.startPipeline(command.sessionId, "default-implementation");
        break;
      case "new-session":
        // Real create flow already lives in SessionsSurface; this just navigates there.
        setActiveId("sessions");
        break;
      case "run-retro":
        setActiveId("retro");
        break;
      case "toggle-shell":
      case "open-settings":
        break; // no-op: no shell-toggle or settings surface exists yet
    }
  }

  return (
    <div className={styles.shell}>
      <IconRail items={RAIL_ITEMS} activeId={activeId} onSelect={setActiveId} />
      {(() => {
        if (activeId === "sessions") {
          return <SessionsSurface jumpToSessionId={jumpToSessionId} />;
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
      <CommandPalette
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
        commands={commands}
        onDispatch={handleDispatch}
      />
    </div>
  );
}
