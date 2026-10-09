import { useCallback, useEffect, useState } from "react";
import { Button } from "../../../components/Button/Button";
import { Toast } from "../../../components/Toast/Toast";
import type { PipelineClient } from "../../../pipeline/client/pipelineClient";
import type { RouterClient } from "../../../router/client";
import { subscribeToEvents } from "../../../router/events";
import {
  SESSION_SHELL_REFETCH_EVENT_TYPES,
  WORKSPACE_SESSION_EVENT_TYPES,
} from "../../../router/sessionEventTypes";
import type { Session } from "../../../router/types";
import {
  EMPTY_OPEN_TASK_BREAKDOWN,
  totalOpenTasks,
  type OpenTaskBreakdown,
  type SessionAgentState,
} from "../rail/railGroups";
import { SessionActionsMenu, shellMenuState } from "../actions/SessionActionsMenu";
import { SessionDeleteConfirmDialog } from "../actions/SessionDeleteConfirmDialog";
import { useShellDockState } from "../../../conversation/ShellDock/dockState";
import { useLiveShells } from "../../../conversation/ShellDock/useLiveShells";
import { requestShellToggle } from "../../../conversation/shellToggleSignal";
import { requestStreamReattach } from "../../../conversation/streamReattachSignal";
import { WORKSPACE_TABS, type WorkspaceTabId } from "./tabRegistry";
import { RUN_EVENT_TYPES } from "./usePipelineRunning";
import styles from "./SessionWorkspace.module.css";

export interface SessionWorkspaceProps {
  sessionId: string;
  routerClient: RouterClient;
  pipelineClient: PipelineClient;
  /** This session's open work, computed once by the caller (SessionsSurface already runs useOpenTaskCounts) — not refetched here. Bucketed, not a bare count, because the delete dialog names the buckets. */
  openTaskBreakdown?: OpenTaskBreakdown;
  /** This session's live agent state (useSessionAgentState) — only the delete dialog reads it, and only to say what the turn in flight is doing. */
  agentState?: SessionAgentState;
  initialTab?: WorkspaceTabId;
  /**
   * Sleep and wake this session. Not done here: both go through the one
   * controller the surface owns (useSessionSleep), which is what makes the
   * kebab's "Sleep now" ask about unlanded commits exactly as the rail
   * card's ⏸ does. Before that, this shell called terminate itself and the
   * question went unasked from the one place you were most likely to have
   * commits waiting.
   */
  onSleep: (session: Session) => void;
  onWake: (session: Session) => void;
  /** Leave this session's workspace for the Launchpad — not a lifecycle action. */
  onClose: () => void;
  /** Set for a session just created from the Launchpad — forwarded into the Conversation tab's context only. */
  provisioning?: boolean;
  onProvisioningDone?: () => void;
  pendingFirstMessage?: string;
  onPendingFirstMessageHandled?: () => void;
}

/**
 * Thin per-session shell: back control, name, tab bar. (No activity chip:
 * the rail card's status line already says whether the session is working,
 * sleeping or done, in more useful words than "active".) Tabs
 * lazy-mount on first visit and stay mounted (hidden) after that so a
 * visited tab's own state (composer draft, graph toggle, scroll) survives
 * flipping between tabs — see tabRegistry.tsx for the tab definitions.
 */
export function SessionWorkspace({
  sessionId,
  routerClient,
  pipelineClient,
  openTaskBreakdown = EMPTY_OPEN_TASK_BREAKDOWN,
  agentState,
  initialTab,
  onSleep,
  onWake,
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
  // The shell menu item reads "Open"/"Show"/"Hide" from these two: the host knows whether a shell lives, the dock store whether it is showing.
  const shellDock = useShellDockState(sessionId);
  const liveShells = useLiveShells();
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const [actionError, setActionError] = useState<string>();
  const [running, setRunning] = useState(false);
  const [restarting, setRestarting] = useState(false);
  const [editingName, setEditingName] = useState(false);
  const [draftName, setDraftName] = useState("");
  const [focusFilePath, setFocusFilePath] = useState<string>();
  const [focusCommitFile, setFocusCommitFile] = useState<{ hash: string; path: string }>();
  const openTaskCount = totalOpenTasks(openTaskBreakdown);

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

  // BounceSession (supervisor.go) returns early with no respawn if
  // terminate fails (e.g. the child is busy past terminateGrace) — a
  // swallowed failure here leaves a dead agent with zero indication. On
  // success, force a stream reattach rather than relying on the cached
  // session.activity value: the record can still read "inactive" the
  // instant after a bounce, and a permanently silent transcript reads
  // identically to "nothing happened".
  function restartAgent() {
    setMenuOpen(false);
    setRestarting(true);
    routerClient
      .bounce(sessionId)
      .then(() => {
        refetchSession();
        requestStreamReattach(sessionId);
      })
      .catch((err: unknown) => {
        setActionError(err instanceof Error ? err.message : String(err));
      })
      .finally(() => setRestarting(false));
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

  // A touched-file click in the Conversation tab's rail routes here: surface
  // the Files tab (mounting it if unvisited) and hand it the file to diff.
  function openFileDiff(path: string) {
    setFocusCommitFile(undefined);
    setFocusFilePath(path);
    selectTab("files");
  }

  // The auto-merged Land card's "See the changes": a file as the step-1
  // merge commit left it, against the session's branch before it.
  function openCommitFileDiff(hash: string, path: string) {
    setFocusFilePath(undefined);
    setFocusCommitFile({ hash, path });
    selectTab("files");
  }

  function commitName() {
    setEditingName(false);
    const trimmed = draftName.trim();
    if (!session || !trimmed || trimmed === session.name) return;
    setSession({ ...session, name: trimmed }); // optimistic
    routerClient
      .renameSession(sessionId, trimmed)
      .then(setSession)
      .catch((err: unknown) => {
        setActionError(err instanceof Error ? err.message : String(err));
        refetchSession(); // revert to server truth on failure
      });
  }

  return (
    <div className={styles.workspace}>
      <div className={styles.bar}>
        <span className={styles.name}>
          {editingName && session ? (
            <input
              className={styles.nameInput}
              value={draftName}
              autoFocus
              aria-label="Session name"
              onChange={(e) => setDraftName(e.target.value)}
              onBlur={commitName}
              onKeyDown={(e) => {
                if (e.key === "Enter") commitName();
                if (e.key === "Escape") setEditingName(false);
              }}
            />
          ) : (
            <button
              type="button"
              className={styles.nameButton}
              title="Rename session"
              disabled={!session}
              onClick={() => {
                if (!session) return;
                setDraftName(session.name || "");
                setEditingName(true);
              }}
            >
              {session ? session.name || "Untitled session" : "Loading…"}
            </button>
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
                // The badge counts OPEN work only (running + ready, which
                // includes needs_review, + blocked); done and discarded
                // tasks never count. A bare number reads as the total or as
                // the done count, and matches neither, so the tooltip says
                // what it is.
                <span className={styles.badge} aria-hidden="true" title={openTasksTitle(openTaskBreakdown)}>
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
              shellState={shellMenuState(shellDock.open, liveShells.some((s) => s.sessionId === sessionId))}
              onToggleShell={() => {
                setMenuOpen(false);
                // Shell lives in the (possibly hidden) Conversation tab — surface
                // it first, then toggle the dock via the cross-tree signal.
                selectTab("conversation");
                requestShellToggle();
              }}
              onRestartAgent={restartAgent}
              restartingAgent={restarting}
              onSleep={() => {
                setMenuOpen(false);
                onSleep(session);
              }}
              onWake={() => {
                setMenuOpen(false);
                onWake(session);
              }}
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

      {session && (
        <SessionDeleteConfirmDialog
          open={confirmingDelete}
          session={session}
          agentState={agentState}
          openTaskBreakdown={openTaskBreakdown}
          routerClient={routerClient}
          onConfirm={() => void confirmDelete()}
          onCancel={() => setConfirmingDelete(false)}
        />
      )}

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
            provisioning={provisioning}
            onProvisioningDone={onProvisioningDone}
            pendingFirstMessage={pendingFirstMessage}
            onPendingFirstMessageHandled={onPendingFirstMessageHandled}
            onOpenFile={openFileDiff}
            focusFilePath={focusFilePath}
            onOpenCommitFile={openCommitFileDiff}
            focusCommitFile={focusCommitFile}
          />
        </div>
      ))}
    </div>
  );
}

/** "4 open tasks: 1 running, 3 ready" — the Tasks badge's tooltip. */
export function openTasksTitle(b: OpenTaskBreakdown): string {
  const total = totalOpenTasks(b);
  const parts = [
    b.running > 0 && `${b.running} running`,
    b.ready > 0 && `${b.ready} ready`,
    b.blocked > 0 && `${b.blocked} blocked`,
  ].filter(Boolean);
  return `${total} open ${total === 1 ? "task" : "tasks"}: ${parts.join(", ")}`;
}
