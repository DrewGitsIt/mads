import { useEffect, useState } from "react";
import { Button } from "../../components/Button/Button";
import { Toast } from "../../components/Toast/Toast";
import { useElectronPtyBridge } from "../../conversation/TerminalRenderer/ptyBridge";
import type { PipelineClient } from "../../pipeline/client/pipelineClient";
import { useWorkspaces } from "../../library/workspaces/useWorkspaces";
import { router } from "../../router/client";
import type { Session } from "../../router/types";
import { NoSessionsEmptyState } from "./EmptyStates";
import { Launchpad } from "./launchpad/Launchpad";
import { SessionRail } from "./rail/SessionRail";
import { EMPTY_OPEN_TASK_BREAKDOWN, totalOpenTasks } from "./rail/railGroups";
import { useOpenTaskCounts } from "./state/useOpenTaskCounts";
import { useSessionAgentState } from "./state/useSessionAgentState";
import { useSessionLinks } from "./state/useSessionLinks";
import { useSessionsList } from "./state/useSessionsList";
import { SessionWorkspace } from "./workspace/SessionWorkspace";
import type { WorkspaceTabId } from "./workspace/tabRegistry";
import styles from "./SessionsSurface.module.css";

export interface SessionsSurfaceProps {
  jumpToSessionId?: string;
  jumpToTab?: WorkspaceTabId;
  pipelineClient: PipelineClient;
}

/** Top-level Sessions surface (registered in App.tsx) — owns the shared session list so the rail and launchpad don't duplicate fetch/subscribe logic. */
export function SessionsSurface({ jumpToSessionId, jumpToTab, pipelineClient }: SessionsSurfaceProps) {
  const { sessions, loading, error, createSession } = useSessionsList();
  const openTaskCounts = useOpenTaskCounts();
  const links = useSessionLinks(sessions.map((s) => s.id));
  // Only the rail's adaptive context labels need these: a workspace's
  // primary repo and default persona are what decide whether a card's cwd
  // and agent are worth printing.
  const { workspaces } = useWorkspaces();
  const [selectedId, setSelectedId] = useState<string>();
  // One subscription for the whole rail, however many cards are listed —
  // opening a session is also what retires its sticky "Done · your turn".
  const agentStates = useSessionAgentState(selectedId);
  const [initialTab, setInitialTab] = useState<WorkspaceTabId>();
  const [actionError, setActionError] = useState<{ title: string; message: string }>();
  const [provisioningSessionId, setProvisioningSessionId] = useState<string>();
  const [pendingFirstMessage, setPendingFirstMessage] =
    useState<{ sessionId: string; text: string }>();
  const ptyBridge = useElectronPtyBridge();

  useEffect(() => {
    if (jumpToSessionId) {
      setSelectedId(jumpToSessionId);
      setInitialTab(jumpToTab);
    }
  }, [jumpToSessionId, jumpToTab]);

  /**
   * A session action (resume/delete) can fail for a session that isn't the
   * one on screen — it's triggered from the rail, not the open workspace. Name
   * the session in the toast so its error can't read as the current session's.
   */
  function reportActionError(verb: string, sessionId: string, err: unknown) {
    const name = sessions.find((s) => s.id === sessionId)?.name || "Untitled session";
    setActionError({
      title: `Couldn't ${verb} “${name}”`,
      message: err instanceof Error ? err.message : String(err),
    });
  }

  function handleSessionCreated(session: Session, goalText: string) {
    setSelectedId(session.id);
    setInitialTab(undefined);
    setProvisioningSessionId(session.id);
    setPendingFirstMessage({ sessionId: session.id, text: goalText });
  }

  return (
    <div className={styles.surface}>
      <aside className={styles.rail}>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => {
            setSelectedId(undefined);
            setActionError(undefined);
          }}
        >
          + New session
        </Button>
        {!loading && sessions.length === 0 ? (
          <NoSessionsEmptyState />
        ) : (
          <SessionRail
            sessions={sessions}
            openTaskCounts={openTaskCounts}
            agentStates={agentStates}
            links={links}
            workspaces={workspaces}
            routerClient={router}
            selectedId={selectedId}
            onSelect={(id) => {
              setSelectedId((cur) => (cur === id ? undefined : id));
              setInitialTab(undefined);
              // A prior action's error toast is scoped to the surface, not the
              // session it named — clear it on navigation so it can't linger
              // over an unrelated session.
              setActionError(undefined);
            }}
            onResume={(id) => {
              router.resumeSession(id).catch((err: unknown) => {
                reportActionError("resume", id, err);
              });
            }}
            onClose={(id) => {
              router.closeSession(id).catch((err: unknown) => {
                reportActionError("close", id, err);
              });
            }}
            onDelete={(id) => {
              router.deleteSession(id).catch((err: unknown) => {
                reportActionError("delete", id, err);
              });
            }}
          />
        )}
      </aside>
      <main className={styles.main}>
        {selectedId ? (
          <SessionWorkspace
            key={selectedId}
            sessionId={selectedId}
            routerClient={router}
            pipelineClient={pipelineClient}
            ptyBridge={ptyBridge}
            openTaskCount={totalOpenTasks(openTaskCounts[selectedId] ?? EMPTY_OPEN_TASK_BREAKDOWN)}
            initialTab={initialTab}
            onClose={() => setSelectedId(undefined)}
            provisioning={selectedId === provisioningSessionId}
            onProvisioningDone={() => setProvisioningSessionId(undefined)}
            pendingFirstMessage={
              selectedId && selectedId === pendingFirstMessage?.sessionId
                ? pendingFirstMessage.text
                : undefined
            }
            onPendingFirstMessageHandled={() => setPendingFirstMessage(undefined)}
          />
        ) : (
          <Launchpad
            createSession={createSession}
            onSessionCreated={handleSessionCreated}
            sessions={sessions}
          />
        )}
        {error && <Toast tone="bad" title="Couldn't load sessions" description={error} />}
        {actionError && (
          <Toast
            tone="bad"
            title={actionError.title}
            description={actionError.message}
            onDismiss={() => setActionError(undefined)}
          />
        )}
      </main>
    </div>
  );
}
