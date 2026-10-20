import { useEffect, useMemo, useRef, useState } from "react";
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
import { useDoneMotion } from "./rail/useDoneMotion";
import { EMPTY_AGENT_STATE, EMPTY_OPEN_TASK_BREAKDOWN, statusOf } from "./rail/railGroups";
import { LinkModal, type NodeStatus } from "./links/LinkModal";
import { useLinkEditor, useRailLinkEditor } from "./links/useLinkEditor";
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
import { FullWidthContext, type FullWidthApi } from "./fullWidth";
import styles from "./SessionsSurface.module.css";

export interface SessionsSurfaceProps {
  jumpToSessionId?: string;
  jumpToTab?: WorkspaceTabId;
  /** With jumpToTab "tasks": the run to show (a run_seq, or "new" for Plan). A new object each time, so a repeat jump re-applies. */
  jumpToRun?: { run: number | "new" };
  pipelineClient: PipelineClient;
}

/** Top-level Sessions surface (registered in App.tsx) — owns the shared session list so the rail and launchpad don't duplicate fetch/subscribe logic. */
export function SessionsSurface({ jumpToSessionId, jumpToTab, jumpToRun, pipelineClient }: SessionsSurfaceProps) {
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
  const [openRun, setOpenRun] = useState<{ run: number | "new" }>();
  const [actionError, setActionError] = useState<{ title: string; message: string }>();
  const [provisioningSessionId, setProvisioningSessionId] = useState<string>();
  const [pendingFirstMessage, setPendingFirstMessage] =
    useState<{ sessionId: string; text: string }>();

  useEffect(() => {
    if (jumpToSessionId) {
      setSelectedId(jumpToSessionId);
      setOpenDelegate(undefined);
      setInitialTab(jumpToTab);
      setOpenRun(jumpToRun);
    }
  }, [jumpToSessionId, jumpToTab, jumpToRun]);

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
  // The ✓ and bring-back's motion on the rail: the controller says when a
  // request goes out and what the server answered, the rail draws it.
  const doneMotion = useDoneMotion();
  const sleep = useSessionSleep(
    router,
    (verb, session, err) => reportActionError(verb, session.id, err),
    doneMotion,
  );

  function handleSessionCreated(session: Session, goalText: string) {
    setSelectedId(session.id);
    setOpenDelegate(undefined);
    setInitialTab(undefined);
    setOpenRun(undefined);
    setProvisioningSessionId(session.id);
    // A /new-follower created without a goal has nothing to send.
    setPendingFirstMessage(goalText ? { sessionId: session.id, text: goalText } : undefined);
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

  // The link editor: one draft shared by the modal and the rail, which turns
  // into the editor's picker while it is open.
  const linkEditor = useLinkEditor({ links, sessions, client: router });
  const railLinkEditor = useRailLinkEditor(linkEditor);
  const nodeStatus = (id: string): NodeStatus | undefined => {
    const session = sessions.find((s) => s.id === id);
    if (!session) return undefined;
    const status = statusOf(
      session,
      agentStates[id] ?? EMPTY_AGENT_STATE,
      openTaskCounts[id] ?? EMPTY_OPEN_TASK_BREAKDOWN,
    );
    return { label: status.label, tone: status.tone };
  };
  // The pane under the editor is blurred and out of reach: no pointer, no
  // focus. inert is set by hand — React 18 doesn't know the attribute.
  const paneRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const pane = paneRef.current;
    if (!pane) return;
    if (linkEditor.isOpen) pane.setAttribute("inert", "");
    else pane.removeAttribute("inert");
  }, [linkEditor.isOpen]);

  const [fullWidth, setFullWidth] = useState(false);
  const fullWidthApi = useMemo<FullWidthApi>(() => ({ full: fullWidth, setFull: setFullWidth }), [fullWidth]);

  const surface = (
    <div className={styles.surface} data-full-width={fullWidth || undefined}>
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
                setOpenRun(undefined);
                setActionError(undefined);
                return;
              }
              setSelectedId((cur) => (cur === id ? undefined : id));
              setInitialTab(undefined);
              setOpenRun(undefined);
              // A prior action's error toast is scoped to the surface, not the
              // session it named — clear it on navigation so it can't linger
              // over an unrelated session.
              setActionError(undefined);
            }}
            onSleep={sleep.requestSleep}
            doneMotion={doneMotion}
            onSetDone={(session, done, working) => {
              if (done) sleep.requestDone(session, working);
              else sleep.bringBack(session);
            }}
            onMove={(id, rank) =>
              router.setSessionRailRank(id, rank).then(
                () => undefined,
                (err: unknown) => {
                  reportActionError("move", id, err);
                  throw err;
                },
              )
            }
            onDelete={(id) => {
              router.deleteSession(id).catch((err: unknown) => {
                reportActionError("delete", id, err);
              });
            }}
            onOpenLinks={(id, trigger) => linkEditor.openOn(id, trigger)}
            linkEditor={railLinkEditor}
          />
        )}
      </aside>
      <main className={styles.main}>
        <div
          ref={paneRef}
          className={styles.pane}
          data-session-pane=""
          data-blurred={linkEditor.isOpen || undefined}
          aria-hidden={linkEditor.isOpen || undefined}
        >
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
              openRun={openRun}
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
        </div>
        {linkEditor.isOpen && (
          // A click on the blurred pane around the editor is a close request.
          <div className={styles.linkScrim} onClick={linkEditor.requestClose} data-link-scrim="">
            <LinkModal editor={linkEditor} statusOf={nodeStatus} />
          </div>
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

  return <FullWidthContext.Provider value={fullWidthApi}>{surface}</FullWidthContext.Provider>;
}
