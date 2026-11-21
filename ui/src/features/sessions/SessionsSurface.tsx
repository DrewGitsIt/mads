import { useEffect, useMemo, useRef, useState } from "react";
import { Button } from "../../components/Button/Button";
import { Banner } from "../../components/Banner/Banner";
import { Toast } from "../../components/Toast/Toast";
import type { PipelineClient } from "../../pipeline/client/pipelineClient";
import { useAgents } from "../../library/agents/useAgents";
import { useWorkspaces } from "../../library/workspaces/useWorkspaces";
import { router } from "../../router/client";
import { HostScopeProvider } from "../../router/hostScope";
import { OPEN_CREATED_SESSION_EVENT, type CreatedSessionDetail } from "../../appNavigation";
import type { Delegate, Session } from "../../router/types";
import { NoSessionsEmptyState } from "./EmptyStates";
import { Launchpad } from "./launchpad/Launchpad";
import { SessionRail } from "./rail/SessionRail";
import { DeviceScopeBar } from "./rail/DeviceScopeBar";
import { useDoneMotion } from "./rail/useDoneMotion";
import { EMPTY_AGENT_STATE, EMPTY_OPEN_TASK_BREAKDOWN, statusOf } from "./rail/railGroups";
import { LinkModal, type NodeStatus } from "./links/LinkModal";
import { useLinkEditor, useRailLinkEditor } from "./links/useLinkEditor";
import { SessionSleepPrompt, useSessionSleep } from "./actions/useSessionSleep";
import { useOpenTaskCounts } from "./state/useOpenTaskCounts";
import { useSessionAgentState } from "./state/useSessionAgentState";
import { useOpenDelegateRecords } from "./state/useOpenDelegateRecords";
import { useSessionLinks } from "./state/useSessionLinks";
import { useSessionsList } from "./state/useSessionsList";
import { useSelectionFollowsList } from "./state/selectionFollowsList";
import { hostKey, useMesh, useSessionLive } from "../../router/mesh";
import { refreshSessions } from "../../router/sessionsStore";
import { usePeerLibrary } from "./state/usePeerLibrary";
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
  const openTaskCounts = useOpenTaskCounts();
  const links = useSessionLinks(sessions.map((s) => s.id));
  // Only the rail's adaptive context labels need these: a workspace's
  // primary repo and default persona are what decide whether a card's cwd
  // and agent are worth printing — and the persona list is what turns the
  // `agt_…` reference a session record stores into a name worth printing at
  // all (router/agentRef.ts).
  const { workspaces: localWorkspaces } = useWorkspaces();
  const { agents: localAgents } = useAgents();
  // On All devices, a remote session's labels come from its own device.
  const { workspaces, agents } = usePeerLibrary(localWorkspaces, localAgents);
  const [selectedId, setSelectedId] = useState<string>();
  // A delegate (harness-run subagent) open in the main pane in place of its
  // parent's workspace — opened from its row in the rail.
  const [openDelegate, setOpenDelegate] = useState<{ sessionId: string; harnessId: string }>();
  // Its own records, past the fade window: the rail lists an open
  // delegate's finished subagents in full, and keeps its parents' rows.
  const openDelegateRecords = useOpenDelegateRecords(router, openDelegate);
  const selectedDelegate = useMemo(
    () => (openDelegate ? { ...openDelegate, records: openDelegateRecords } : undefined),
    [openDelegate, openDelegateRecords],
  );
  const delegateSettings = useDelegateSettings(router);
  const fadeMs = delegateFadeMs(delegateSettings.fade);
  // One subscription for the whole rail, however many cards are listed. The
  // list is handed in too: each record mirrors its session's latest agent
  // state, which is what makes a reload mid-turn correct before the next
  // transition arrives. Opening a session retires its sticky "Done · your turn".
  const agentStates = useSessionAgentState(sessions, selectedId, fadeMs);
  const [initialTab, setInitialTab] = useState<WorkspaceTabId>();
  const [openRun, setOpenRun] = useState<{ run: number | "new" }>();
  const [actionError, setActionError] = useState<{ title: string; message: string; sessionId: string }>();
  // Opening one, from its rail row or from its card in another delegate's
  // stream (a subagent's own subagent): the same path, so the rail selects it.
  const openDelegateRecord = (d: Delegate) => {
    setOpenDelegate({ sessionId: d.session_id, harnessId: d.harness_id });
    setSelectedId(d.session_id);
    setActionError(undefined);
  };
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
      sessionId,
    });
  }

  // One controller for every control that sleeps a session — the workspace
  // kebab's "Sleep now" and the rail's ✓ (Done + sleep) — so the
  // unlanded-commits question is asked from either, and the prompt below is
  // the only one of its kind on screen.
  // The ✓ and bring-back's motion on the rail: the controller says when a
  // request goes out and what the server answered, the rail draws it.
  // Selection follows the list: when the open session goes (archived,
  // deleted anywhere, checked off), its neighbour in the rail opens, else the
  // Launchpad (selectionFollowsList.ts).
  const openSessionId = openDelegate?.sessionId ?? selectedId;
  const follows = useSelectionFollowsList({
    sessions,
    loading,
    error,
    openSessionId,
    select: (id) => {
      setOpenDelegate(undefined);
      setSelectedId(id);
      setInitialTab(undefined);
      setOpenRun(undefined);
    },
  });
  // Mesh mode: changing which devices the list shows never changes the pane.
  // A remote session the new scope hides stays open, off-list, until the user
  // picks something else.
  const { scope: meshScope } = useMesh();
  const lastScope = useRef(meshScope);
  useEffect(() => {
    if (lastScope.current === meshScope) return;
    lastScope.current = meshScope;
    follows.keepOffList(openSessionId);
  }, [meshScope, openSessionId, follows]);
  const openSessionRef = useRef(openSessionId);
  openSessionRef.current = openSessionId;

  /**
   * Archive/Delete, from the rail or the workspace menu. Deleting the session
   * on screen opens its neighbour *first*: unmounting its workspace stops every
   * fetch and subscription, where moving after the DELETE answered left its
   * own session.deleted to re-read a session that no longer exists (404s).
   * A refused delete says so; the session is still in the list to go back to.
   */
  function deleteSession(id: string) {
    if (id === openSessionRef.current) follows.leave(id);
    router
      .deleteSession(id)
      // A peer's session.deleted never reaches this window's bus (mesh
      // mode): re-read now rather than at the next poll.
      .then(() => refreshSessions())
      .catch((err: unknown) => {
        reportActionError("delete", id, err);
      });
  }

  const doneMotion = useDoneMotion();
  // ✓ on the open session moves on once the server has it — not on the
  // click, so a refused Done or a dismissed stop/land question stays put.
  const doneNotices = useMemo(
    () => ({
      sent: doneMotion.sent,
      failed: doneMotion.failed,
      confirmed: (updated: Session, done: boolean) => {
        doneMotion.confirmed(updated, done);
        if (done && updated.id === openSessionRef.current) follows.leave(updated.id);
      },
    }),
    [doneMotion, follows],
  );
  const sleep = useSessionSleep(
    router,
    (verb, session, err) => reportActionError(verb, session.id, err),
    doneNotices,
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
  // The device the open session (or delegate) lives on, for its host scope
  // and its key below.
  const openHost = sessions.find((s) => s.id === (openDelegate?.sessionId ?? selectedId))?.host;
  // Bumped when a remote session's bus recovers (its host updated, or let
  // this device back in): what the pane read meanwhile wasn't ordered
  // against that bus, so it remounts and reads again.
  const openLive = useSessionLive(openDelegate?.sessionId ?? selectedId);
  const paneKey = `${hostKey(openHost)}/${openLive.epoch}`;
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
        {/* With a list already on screen this is a failed refresh, not a
            failed load — say so, or the banner contradicts the visible rail. */}
        <Banner
          active={!!error}
          tone="bad"
          variant="compact"
          className={styles.railBanner}
          title={sessions.length > 0 ? "Couldn't refresh sessions" : "Couldn't load sessions"}
          description={`${sessions.length > 0 ? "The list may be out of date. " : ""}Retrying automatically. (${error})`}
          action={{ label: "Retry now", onClick: retry }}
          resolvedTitle={sessions.length > 0 ? "Sessions refreshed" : undefined}
        />
        {!loading && sessions.length === 0 ? (
          <>
            {/* Nothing here yet, but maybe on another device: the toggle has
                to be reachable from the empty state too (mesh mode). */}
            <DeviceScopeBar />
            <NoSessionsEmptyState />
          </>
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
            selectedDelegate={selectedDelegate}
            onOpenDelegate={openDelegateRecord}
            onVisibleOrder={follows.reportOrder}
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
            onDelete={deleteSession}
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
          {/* Mesh mode: per-host reads and machine-local features follow the
              open session's device (docs/MESH-MODE-DESIGN.md D3b, D5). Keyed
              by that device too: when a session's host is learned after it
              opened (a restored selection before the All devices list lands),
              its subscriptions move to the host's event bus, and remounting
              redoes every mount read behind that bus's ready barrier (THE
              CONTRACT in router/events.ts). */}
          <HostScopeProvider host={openHost}>
          {openDelegate ? (
            <DelegateView
              key={`${paneKey}/${openDelegate.sessionId}/${openDelegate.harnessId}`}
              sessionId={openDelegate.sessionId}
              harnessId={openDelegate.harnessId}
              parent={sessions.find((s) => s.id === openDelegate.sessionId)}
              routerClient={router}
              onBack={() => {
                setSelectedId(openDelegate.sessionId);
                setOpenDelegate(undefined);
              }}
              onOpenDelegate={openDelegateRecord}
            />
          ) : selectedId ? (
            <SessionWorkspace
              key={`${paneKey}/${selectedId}`}
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
              onLeave={() => follows.leave(selectedId)}
              onDelete={() => deleteSession(selectedId)}
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
          </HostScopeProvider>
        </div>
        {linkEditor.isOpen && (
          // A click on the blurred pane around the editor is a close request.
          <div className={styles.linkScrim} onClick={linkEditor.requestClose} data-link-scrim="">
            <LinkModal editor={linkEditor} statusOf={nodeStatus} />
          </div>
        )}
        <SessionSleepPrompt sleep={sleep} routerClient={router} />
        {actionError && (
          <Toast
            tone="bad"
            title={actionError.title}
            description={actionError.message}
            target={
              actionError.sessionId !== selectedId || openDelegate
                ? {
                    label: "Go to session",
                    onClick: () => {
                      setOpenDelegate(undefined);
                      setSelectedId(actionError.sessionId);
                      setInitialTab(undefined);
                      setOpenRun(undefined);
                    },
                  }
                : undefined
            }
            onDismiss={() => setActionError(undefined)}
          />
        )}
      </main>
    </div>
  );

  return <FullWidthContext.Provider value={fullWidthApi}>{surface}</FullWidthContext.Provider>;
}
