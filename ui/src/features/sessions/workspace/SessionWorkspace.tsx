import { useCallback, useEffect, useState } from "react";
import { Button } from "../../../components/Button/Button";
import { Chip } from "../../../components/Chip/Chip";
import { Toast } from "../../../components/Toast/Toast";
import type { PtyBridge } from "../../../conversation/TerminalRenderer/ptyBridge";
import type { PipelineClient } from "../../../pipeline/client/pipelineClient";
import type { RouterClient } from "../../../router/client";
import { subscribeToEvents } from "../../../router/events";
import {
  SESSION_SHELL_REFETCH_EVENT_TYPES,
  WORKSPACE_SESSION_EVENT_TYPES,
} from "../../../router/sessionEventTypes";
import type { Session } from "../../../router/types";
import { SessionActionsMenu } from "../actions/SessionActionsMenu";
import { SessionDeleteConfirmDialog } from "../actions/SessionDeleteConfirmDialog";
import { WORKSPACE_TABS, type WorkspaceTabId } from "./tabRegistry";
import { RUN_EVENT_TYPES } from "./usePipelineRunning";
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
  /** Set for a session just created from the Launchpad — forwarded into the Conversation tab's context only. */
  provisioning?: boolean;
  onProvisioningDone?: () => void;
  pendingFirstMessage?: string;
  onPendingFirstMessageHandled?: () => void;
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
  provisioning,
  onProvisioningDone,
  pendingFirstMessage,
  onPendingFirstMessageHandled,
}: SessionWorkspaceProps) {
  const [session, setSession] = useState<Session | null>(null);
  const [activeTab, setActiveTab] = useState<WorkspaceTabId>(initialTab ?? "conversation");
  const [visited, setVisited] = useState<Set<WorkspaceTabId>>(
    () => new Set([initialTab ?? "conversation"]),
  );
  const [menuOpen, setMenuOpen] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const [actionError, setActionError] = useState<string>();
  const [running, setRunning] = useState(false);

  const refetchSession = useCallback(() => {
    void routerClient.getSession(sessionId).then(setSession);
  }, [routerClient, sessionId]);

  // One session-scoped subscription for everything this shell and its
  // Conversation tab need (session lifecycle, linked-session inject,
  // pipeline run status) instead of three — see
  // WORKSPACE_SESSION_EVENT_TYPES's own comment for why.
  useEffect(() => {
    refetchSession();
    setRunning(false);
    return subscribeToEvents(
      { sessionId, types: WORKSPACE_SESSION_EVENT_TYPES },
      (envelope) => {
        if ((RUN_EVENT_TYPES as string[]).includes(envelope.type)) {
          setRunning(envelope.type === "pipeline.started");
        } else if ((SESSION_SHELL_REFETCH_EVENT_TYPES as string[]).includes(envelope.type)) {
          refetchSession();
        }
        // Anything else in WORKSPACE_SESSION_EVENT_TYPES (task.*/stage.*/
        // file.changed/note.created) is there only for usePipelineRun's
        // shared subscription — this shell has nothing to do with it.
      },
    );
  }, [refetchSession, sessionId]);

  function closeSession() {
    setMenuOpen(false);
    routerClient.closeSession(sessionId).catch((err: unknown) => {
      setActionError(err instanceof Error ? err.message : String(err));
    });
  }

  function resumeSession() {
    setMenuOpen(false);
    routerClient.resumeSession(sessionId).catch((err: unknown) => {
      setActionError(err instanceof Error ? err.message : String(err));
    });
  }

  async function confirmDelete() {
    setConfirmingDelete(false);
    try {
      await routerClient.deleteSession(sessionId);
      onClose();
    } catch (err) {
      setActionError(err instanceof Error ? err.message : String(err));
    }
  }

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
          <span className={styles.nameText}>
            {session ? session.name || "Untitled session" : "Loading…"}
          </span>
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

        <div className={styles.kebabWrap}>
          <Button
            variant="ghost"
            size="sm"
            aria-label="Workspace actions"
            onClick={() => setMenuOpen((x) => !x)}
          >
            ⋮
          </Button>
          {menuOpen && session && (
            <SessionActionsMenu
              className={styles.menu}
              sessionId={sessionId}
              activity={session.activity}
              onClose={closeSession}
              onResume={resumeSession}
              onDeleteRequest={() => {
                setMenuOpen(false);
                setConfirmingDelete(true);
              }}
            />
          )}
        </div>
      </div>

      {actionError && (
        <Toast tone="bad" title="Session action failed" description={actionError} />
      )}

      <SessionDeleteConfirmDialog
        open={confirmingDelete}
        onConfirm={() => void confirmDelete()}
        onCancel={() => setConfirmingDelete(false)}
      />

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
            provisioning={provisioning}
            onProvisioningDone={onProvisioningDone}
            pendingFirstMessage={pendingFirstMessage}
            onPendingFirstMessageHandled={onPendingFirstMessageHandled}
          />
        </div>
      ))}
    </div>
  );
}
