import { useEffect, useState } from "react";
import { Button } from "../../../components/Button/Button";
import { Chip } from "../../../components/Chip/Chip";
import type { PtyBridge } from "../../../conversation/TerminalRenderer/ptyBridge";
import type { PipelineClient } from "../../../pipeline/client/pipelineClient";
import type { RouterClient } from "../../../router/client";
import type { Session } from "../../../router/types";
import { WORKSPACE_TABS, type WorkspaceTabId } from "./tabRegistry";
import { usePipelineRunning } from "./usePipelineRunning";
import styles from "./SessionWorkspace.module.css";

export interface SessionWorkspaceProps {
  sessionId: string;
  routerClient: RouterClient;
  pipelineClient: PipelineClient;
  ptyBridge?: PtyBridge;
  /** This session's open-task count, computed once by the caller (SessionsSurface already runs useOpenTaskCounts) — not refetched here. */
  openTaskCount: number;
  initialTab?: WorkspaceTabId;
  onClose: () => void;
}

/**
 * Thin per-session shell: back control, name/activity chip, tab bar. Tabs
 * lazy-mount on first visit and stay mounted (hidden) after that so a
 * visited tab's own state (composer draft, graph toggle, scroll) survives
 * flipping between tabs — see tabRegistry.tsx for the tab definitions.
 */
export function SessionWorkspace({
  sessionId,
  routerClient,
  pipelineClient,
  ptyBridge,
  openTaskCount,
  initialTab,
  onClose,
}: SessionWorkspaceProps) {
  const [session, setSession] = useState<Session | null>(null);
  const [activeTab, setActiveTab] = useState<WorkspaceTabId>(initialTab ?? "conversation");
  const [visited, setVisited] = useState<Set<WorkspaceTabId>>(
    () => new Set([initialTab ?? "conversation"]),
  );
  const running = usePipelineRunning(sessionId);

  useEffect(() => {
    let cancelled = false;
    void routerClient.getSession(sessionId).then((s) => {
      if (!cancelled) setSession(s);
    });
    return () => {
      cancelled = true;
    };
  }, [routerClient, sessionId]);

  function selectTab(id: WorkspaceTabId) {
    setActiveTab(id);
    setVisited((prev) => (prev.has(id) ? prev : new Set(prev).add(id)));
  }

  return (
    <div className={styles.workspace}>
      <div className={styles.bar}>
        <Button variant="ghost" size="sm" aria-label="Back to Launchpad" onClick={onClose}>
          ✕
        </Button>
        <span className={styles.name}>
          {session ? session.name || "Untitled session" : "Loading…"}
          {session && (
            <Chip tone={session.activity === "active" ? "ok" : "neutral"}>{session.activity}</Chip>
          )}
        </span>
        <nav className={styles.tabs} role="tablist">
          {WORKSPACE_TABS.map((tab) => (
            <button
              key={tab.id}
              type="button"
              role="tab"
              id={`workspace-tab-${tab.id}`}
              aria-selected={tab.id === activeTab}
              aria-controls={`workspace-panel-${tab.id}`}
              aria-label={tab.label}
              className={styles.tab}
              onClick={() => selectTab(tab.id)}
            >
              {tab.label}
              {tab.id === "tasks" && openTaskCount > 0 && (
                <span className={styles.badge} aria-hidden="true">
                  {openTaskCount}
                </span>
              )}
              {tab.id === "pipeline" && running && (
                <span className={styles.rundot} aria-hidden="true" title="run in progress" />
              )}
            </button>
          ))}
        </nav>
        <span className={styles.spacer} />
      </div>

      {WORKSPACE_TABS.filter((tab) => visited.has(tab.id)).map((tab) => (
        <div
          key={tab.id}
          role="tabpanel"
          id={`workspace-panel-${tab.id}`}
          aria-labelledby={`workspace-tab-${tab.id}`}
          className={tab.id === activeTab ? styles.paneActive : styles.pane}
        >
          <tab.component
            sessionId={sessionId}
            routerClient={routerClient}
            pipelineClient={pipelineClient}
            ptyBridge={ptyBridge}
          />
        </div>
      ))}
    </div>
  );
}
