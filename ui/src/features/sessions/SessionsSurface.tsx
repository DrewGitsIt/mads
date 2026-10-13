import { useEffect, useRef, useState } from "react";
import { Button } from "../../components/Button/Button";
import { Toast } from "../../components/Toast/Toast";
import { useBusHealth } from "../../router/useBusHealth";
import type { PipelineClient } from "../../pipeline/client/pipelineClient";
import { useAgents } from "../../library/agents/useAgents";
import { useWorkspaces } from "../../library/workspaces/useWorkspaces";
import { router } from "../../router/client";
import { OPEN_CREATED_SESSION_EVENT, type CreatedSessionDetail } from "../../appNavigation";
import type { Session } from "../../router/types";
import { NoSessionsEmptyState } from "./EmptyStates";
import { Launchpad } from "./launchpad/Launchpad";
import { SessionRail } from "./rail/SessionRail";
import { EMPTY_OPEN_TASK_BREAKDOWN } from "./rail/railGroups";
import { SessionSleepPrompt, useSessionSleep } from "./actions/useSessionSleep";
import { useOpenTaskCounts } from "./state/useOpenTaskCounts";
import { useSessionAgentState } from "./state/useSessionAgentState";
import { useSessionLinks } from "./state/useSessionLinks";
import { useSessionsList } from "./state/useSessionsList";
import { SessionWorkspace } from "./workspace/SessionWorkspace";
import { DelegateView } from "./delegate/DelegateView";
import { useDelegateSettings } from "./state/useDelegateSettings";
import { delegateFadeMs } from "./rail/DelegateRows";
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
  // A delegate (harness-run subagent) open in the main pane in place of its
  // parent's workspace — opened from its row in the rail.
  const [openDelegate, setOpenDelegate] = useState<{ sessionId: string; harnessId: string }>();
  const delegateSettings = useDelegateSettings(router);
  const fadeMs = delegateFadeMs(delegateSettings.fade);
  // One subscription for the whole rail, however many cards are listed. The
  // list is handed in too: each record mirrors its session's latest agent
  // state, which is what makes a reload mid-turn correct before the next
  // transition arrives. Opening a session retires its sticky "Done · your turn".
  const agentStates = useSessionAgentState(sessions, selectedId, fadeMs);
  const [initialTab, setInitialTab] = useState<WorkspaceTabId>();
  const [actionError, setActionError] = useState<{ title: string; message: string }>();
  const [provisioningSessionId, setProvisioningSessionId] = useState<string>();
  const [pendingFirstMessage, setPendingFirstMessage] =
    useState<{ sessionId: string; text: string }>();

  useEffect(() => {
    if (jumpToSessionId) {
      setSelectedId(jumpToSessionId);
      setOpenDelegate(undefined);
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

  // One controller for both sleep controls — the rail card's ⏸ and the
  // workspace kebab's "Sleep now" — so the unlanded-commits question is asked
  // from either, and the prompt below is the only one of its kind on screen.
  const sleep = useSessionSleep(router, (verb, session, err) =>
    reportActionError(verb, session.id, err),
  );

  function handleSessionCreated(session: Session, goalText: string) {
    setSelectedId(session.id);
    setOpenDelegate(undefined);
    setInitialTab(undefined);
    setProvisioningSessionId(session.id);
    setPendingFirstMessage({ sessionId: session.id, text: goalText });
  }

  // The composer's /new card (⌘↵) opens what it created the same way.
  const handleSessionCreatedRef = useRef(handleSessionCreated);
  handleSessionCreatedRef.current = handleSessionCreated;
  useEffect(() => {
    const onCreated = (e: Event) => {
      const { session, goal } = (e as CustomEvent<CreatedSessionDetail>).detail;
      handleSessionCreatedRef.current(session, goal);
    };
    window.addEventListener(OPEN_CREATED_SESSION_EVENT, onCreated);
    return () => window.removeEventListener(OPEN_CREATED_SESSION_EVENT, onCreated);
  }, []);

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
        <div className={styles.railHeader}>
          <Button
            variant="secondary"
            size="md"
            className={styles.newSession}
            onClick={() => {
              setSelectedId(undefined);
              setOpenDelegate(undefined);
              setActionError(undefined);
            }}
          >
            <span className={styles.plus} aria-hidden="true">
              +
            </span>
            New session
          </Button>
        </div>
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
            showDelegates={delegateSettings.show}
            delegateFadeMs={fadeMs}
            selectedDelegate={openDelegate}
            onOpenDelegate={(d) => {
              setOpenDelegate({ sessionId: d.session_id, harnessId: d.harness_id });
              setSelectedId(d.session_id);
              setActionError(undefined);
            }}
            onSelect={(id) => {
              // A card click from a delegate view lands on that session's own
              // workspace — the parent's included, which stays selected.
              if (openDelegate) {
                setOpenDelegate(undefined);
                setSelectedId(id);
                setInitialTab(undefined);
                setActionError(undefined);
                return;
              }
              setSelectedId((cur) => (cur === id ? undefined : id));
              setInitialTab(undefined);
              // A prior action's error toast is scoped to the surface, not the
              // session it named — clear it on navigation so it can't linger
              // over an unrelated session.
              setActionError(undefined);
            }}
            onSleep={sleep.requestSleep}
            onSetAside={(id, aside) => {
              router.setSessionAside(id, aside).catch((err: unknown) => {
                reportActionError(aside ? "set aside" : "bring back", id, err);
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
        {openDelegate ? (
          <DelegateView
            key={`${openDelegate.sessionId}/${openDelegate.harnessId}`}
            sessionId={openDelegate.sessionId}
            harnessId={openDelegate.harnessId}
            parent={sessions.find((s) => s.id === openDelegate.sessionId)}
            routerClient={router}
            onBack={() => {
              setSelectedId(openDelegate.sessionId);
              setOpenDelegate(undefined);
            }}
          />
        ) : selectedId ? (
          <SessionWorkspace
            key={selectedId}
            sessionId={selectedId}
            routerClient={router}
            pipelineClient={pipelineClient}
            openTaskBreakdown={openTaskCounts[selectedId] ?? EMPTY_OPEN_TASK_BREAKDOWN}
            agentState={agentStates[selectedId]}
            initialTab={initialTab}
            onSleep={sleep.requestSleep}
            onWake={sleep.wake}
            onClose={() => setSelectedId(undefined)}
            provisioning={selectedId === provisioningSessionId}
            onProvisioningDone={() => setProvisioningSessionId(undefined)}
            pendingFirstMessage={
              selectedId && selectedId === pendingFirstMessage?.sessionId
                ? pendingFirstMessage.text
                : undefined
            }
            onPendingFirstMessageHandled={() => setPendingFirstMessage(undefined)}
            onOpenDelegate={(harnessId) => setOpenDelegate({ sessionId: selectedId, harnessId })}
          />
        ) : (
          <Launchpad
            createSession={createSession}
            onSessionCreated={handleSessionCreated}
            sessions={sessions}
            links={links}
          />
        )}
        <SessionSleepPrompt sleep={sleep} routerClient={router} />
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
