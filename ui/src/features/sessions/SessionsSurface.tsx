import { useEffect, useState } from "react";
import { Button } from "../../components/Button/Button";
import { Toast } from "../../components/Toast/Toast";
import { useElectronPtyBridge } from "../../conversation/TerminalRenderer/ptyBridge";
import type { PipelineClient } from "../../pipeline/client/pipelineClient";
import { router } from "../../router/client";
import type { Session } from "../../router/types";
import { NoSessionsEmptyState } from "./EmptyStates";
import { Launchpad } from "./launchpad/Launchpad";
import type { Suggestion } from "./launchpad/useSuggestions";
import { SessionRail } from "./rail/SessionRail";
import { EMPTY_OPEN_TASK_BREAKDOWN, totalOpenTasks } from "./rail/railGroups";
import { useOpenTaskCounts } from "./state/useOpenTaskCounts";
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
  const [selectedId, setSelectedId] = useState<string>();
  const [initialTab, setInitialTab] = useState<WorkspaceTabId>();
  const [actionError, setActionError] = useState<string>();
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

  function handleSessionCreated(session: Session, goalText: string) {
    setSelectedId(session.id);
    setInitialTab(undefined);
    setProvisioningSessionId(session.id);
    setPendingFirstMessage({ sessionId: session.id, text: goalText });
  }

  /** "Resume" opens the session as-is; "Pick up" opens it on the Tasks tab so the ready task is in view. Neither seeds composer text. */
  function handleSuggestionPick(suggestion: Suggestion) {
    setSelectedId(suggestion.sessionId);
    setInitialTab(suggestion.kind === "pickup" ? "tasks" : undefined);
    const target = sessions.find((s) => s.id === suggestion.sessionId);
    if (target && target.activity !== "active") {
      router.resumeSession(suggestion.sessionId).catch((err: unknown) => {
        setActionError(err instanceof Error ? err.message : String(err));
      });
    }
  }

  return (
    <div className={styles.surface}>
      <aside className={styles.rail}>
        <Button variant="ghost" size="sm" onClick={() => setSelectedId(undefined)}>
          + New session
        </Button>
        {!loading && sessions.length === 0 ? (
          <NoSessionsEmptyState />
        ) : (
          <SessionRail
            sessions={sessions}
            openTaskCounts={openTaskCounts}
            links={links}
            routerClient={router}
            selectedId={selectedId}
            onSelect={(id) => {
              setSelectedId((cur) => (cur === id ? undefined : id));
              setInitialTab(undefined);
            }}
            onResume={(id) => {
              router.resumeSession(id).catch((err: unknown) => {
                setActionError(err instanceof Error ? err.message : String(err));
              });
            }}
            onDelete={(id) => {
              router.deleteSession(id).catch((err: unknown) => {
                setActionError(err instanceof Error ? err.message : String(err));
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
            onSuggestionPick={handleSuggestionPick}
          />
        )}
        {error && <Toast tone="bad" title="Couldn't load sessions" description={error} />}
        {actionError && (
          <Toast tone="bad" title="Session action failed" description={actionError} />
        )}
      </main>
    </div>
  );
}
