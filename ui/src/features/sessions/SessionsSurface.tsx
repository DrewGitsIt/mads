import { useEffect, useState } from "react";
import { Button } from "../../components/Button/Button";
import { Toast } from "../../components/Toast/Toast";
import { useBusHealth } from "../../router/useBusHealth";
import type { PipelineClient } from "../../pipeline/client/pipelineClient";
import { useAgents } from "../../library/agents/useAgents";
import { useWorkspaces } from "../../library/workspaces/useWorkspaces";
import { router } from "../../router/client";
import type { Session } from "../../router/types";
import { NoSessionsEmptyState } from "./EmptyStates";
import { Launchpad } from "./launchpad/Launchpad";
import { SessionRail } from "./rail/SessionRail";
import { EMPTY_OPEN_TASK_BREAKDOWN } from "./rail/railGroups";
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
  const { sessions, loading, error, retry, createSession } = useSessionsList();
  // The error a user dismissed; a later, different failure shows again.
  const [dismissedError, setDismissedError] = useState<string | null>(null);
  useEffect(() => {
    if (!error) setDismissedError(null);
  }, [error]);
  // The rail, its task counts and agent states are only as fresh as the
  // window's live-event stream: when that stream can't connect they keep
  // showing whatever they last fetched, with nothing on screen to say so. That
  // is how a rail reading "Sleeping" (and a stale context %) sat beside a
  // conversation that was plainly working, 2026-09-30. Say it out loud.
  const busHealth = useBusHealth();
  const [busWarningDismissed, setBusWarningDismissed] = useState(false);
  useEffect(() => {
    // Each new outage gets its own warning, even if the last one was waved off.
    if (busHealth.state === "live") setBusWarningDismissed(false);
  }, [busHealth.state]);
  const openTaskCounts = useOpenTaskCounts();
  const links = useSessionLinks(sessions.map((s) => s.id));
  // Only the rail's adaptive context labels need these: a workspace's
  // primary repo and default persona are what decide whether a card's cwd
  // and agent are worth printing — and the persona list is what turns the
  // `agt_…` reference a session record stores into a name worth printing at
  // all (router/agentRef.ts).
  const { workspaces } = useWorkspaces();
  const { agents } = useAgents();
  const [selectedId, setSelectedId] = useState<string>();
  // One subscription for the whole rail, however many cards are listed. The
  // list is handed in too: each record mirrors its session's latest agent
  // state, which is what makes a reload mid-turn correct before the next
  // transition arrives. Opening a session retires its sticky "Done · your turn".
  const agentStates = useSessionAgentState(sessions, selectedId);
  const [initialTab, setInitialTab] = useState<WorkspaceTabId>();
  const [actionError, setActionError] = useState<{ title: string; message: string }>();
  const [provisioningSessionId, setProvisioningSessionId] = useState<string>();
  const [pendingFirstMessage, setPendingFirstMessage] =
    useState<{ sessionId: string; text: string }>();

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
      {/* data-rail-scroll marks the rail's scroll viewport — this aside owns the
          overflow-y:auto, not the SessionRail div inside it, which is unbounded
          and simply as tall as its content. SessionRow reads the marked box's
          rect to decide whether its hover action cluster (painted fixed,
          outside the card) still has a visible row to hang off; measuring the
          inner div instead would compare a scrolled row against the full
          content height and never dismiss anything. */}
      <aside className={styles.rail} data-rail-scroll="">
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
            agents={agents}
            sessionsLoaded={!loading}
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
            onClose={(id) => {
              router.closeSession(id).catch((err: unknown) => {
                reportActionError("sleep", id, err);
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
            openTaskBreakdown={openTaskCounts[selectedId] ?? EMPTY_OPEN_TASK_BREAKDOWN}
            agentState={agentStates[selectedId]}
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
            links={links}
          />
        )}
        {error && error !== dismissedError && (
          // With a list already on screen this is a failed refresh, not a
          // failed load — say so, or the toast contradicts the visible rail.
          <Toast
            tone="bad"
            title={sessions.length > 0 ? "Couldn't refresh sessions" : "Couldn't load sessions"}
            description={
              sessions.length > 0
                ? `The list may be out of date. Retrying automatically. (${error})`
                : `Retrying automatically. (${error})`
            }
            action={{ label: "Retry now", onClick: retry }}
            onDismiss={() => setDismissedError(error)}
          />
        )}
        {busHealth.state === "failing" && !busWarningDismissed && (
          <Toast
            tone="warn"
            title="Live updates paused"
            description="Can't reach the event stream, so session status, task counts and pipeline state may be out of date. Reconnecting automatically."
            action={{ label: "Reload", onClick: () => window.location.reload() }}
            onDismiss={() => setBusWarningDismissed(true)}
          />
        )}
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
