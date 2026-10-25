import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button } from "../components/Button/Button";
import { Card } from "../components/Card/Card";
import { ConfirmDialog } from "../components/ConfirmDialog/ConfirmDialog";
import { Toast } from "../components/Toast/Toast";
import type { RouterClient, SessionEventSource } from "../router/client";
import { CommitRefusedError, TransportSwitchError } from "../router/client";
import { subscribeToEvents } from "../router/events";
import { getAllEvents } from "../router/eventPages";
import { contextWindowForModel, countsTokens } from "../router/modelDisplay";
import {
  SESSION_SHELL_REFETCH_EVENT_TYPES,
  WORKSPACE_SESSION_EVENT_TYPES,
} from "../router/sessionEventTypes";
import type {
  Delegate,
  AgentState,
  BackgroundTask,
  CommitError,
  QueueChanged,
  QueuedMessage,
  CompactStatus,
  PermissionRequest,
  Session,
  StreamEvent,
  ToolStatus,
  ToolStatusChange,
  ToolUse,
  Usage,
} from "../router/types";
import { useSessionLinks } from "../features/sessions/state/useSessionLinks";
import { leaderLink } from "./Composer/commands/useLinkCommandState";
import { useWorkspaceForRef } from "../tasks/useSessionWorkspace";
import { SessionTerminalBridge } from "./TerminalRenderer/sessionTerminalBridge";
import { SessionDock, latestLandOutcome } from "./Dock";
import { useCheckoutBranch } from "./Dock/useCheckoutBranch";
import { useAgentIndex } from "../router/useAgentIndex";
import { useCompactionSupport } from "../router/compaction";
import { CompactionStatus } from "./CompactionStatus";
import { ChatRenderer } from "./ChatRenderer/ChatRenderer";
import { WorkingIndicator } from "./ChatRenderer/WorkingIndicator";
import { ApprovalJump } from "./ApprovalJump/ApprovalJump";
import type { PermissionReveal } from "./ApprovalJump/permissionReveal";
import { ActivityDock, type DockView } from "./activity/ActivityDock";
import { RunningStack } from "./activity/RunningStack";
import type { SubstripEntry } from "./activity/Substrip";
import {
  applyTaskEvent,
  CONVERSATION_EVENT_TYPES,
  SUBAGENT_STREAM_EVENT_TYPES,
  finishRunningTasks,
  foldHistoryEvents,
  rowsSubStream,
  transcriptSubStream,
  mergeDelegates,
  replayedCompactStatus,
  resumeRunningTasks,
  tasksByToolUse,
  type ConversationHistory,
  type SubStreamItem,
  type TaskModel,
} from "./activity/conversationItems";
import { subagentRows } from "./keyed/conversation";
import { useKeyedConversation } from "./keyed/useKeyedConversation";
import { useReveal } from "./keyed/useReveal";
import { LinkNamesProvider, useLinkNames } from "./activity/linkNames";
import { formatTokens, toolTitle } from "./activity/toolDisplay";
import { Chip } from "../components/Chip/Chip";
import { FADE_OUT_MS, usePresence } from "../components/Motion";
import { streamPaceToWpm, useStreamSpeed } from "./streaming/streamSpeed";
import { PermissionCard } from "./ChatRenderer/PermissionCard";
import { TerminalView, type TerminalViewHandle } from "./TerminalRenderer/TerminalView";
import { formatBracketedPaste } from "./TerminalRenderer/bracketedPaste";
import { Composer } from "./Composer/Composer";
import { ExpandedComposer } from "./Composer/ExpandedComposer";
import { QueuedMessages } from "./Composer/QueuedMessages";
import { matches } from "../platform/hotkeys";
import { ProvisioningStrip } from "./ProvisioningStrip";
import { ShellDock } from "./ShellDock/ShellDock";
import { ShellBridge, type ShellDockBridge } from "./ShellDock/shellBridge";
import { getShellDockState, setShellDockState, useShellDockState } from "./ShellDock/dockState";
import { CommitDialog } from "./Worktree/CommitDialog";
import { useCommitSettings } from "./Worktree/useCommitSettings";
import { useWorktreeCards } from "./Worktree/useWorktreeCards";
import { SHELL_TOGGLE_EVENT } from "./shellToggleSignal";
import { STREAM_REATTACH_EVENT, type StreamReattachDetail } from "./streamReattachSignal";
import { useStickToBottom } from "./useStickToBottom";
import { useOlderPages } from "./useOlderPages";
import { FindBar, useFindBar } from "./Find/FindBar";
import { useVisibleCards } from "./useVisibleCards";
import { useDelegateFile } from "./activity/useDelegateFile";
import styles from "./ConversationTab.module.css";

// When the user last dismissed this session's compaction banner — lets a
// replayed failure (see replayedCompactStatus) stay acknowledged across
// remounts instead of reappearing every time the conversation is opened.
const compactDismissedKey = (sessionId: string) => `mads.compactDismissedAt.${sessionId}`;

function readCompactDismissedAt(sessionId: string): string | undefined {
  try {
    return localStorage.getItem(compactDismissedKey(sessionId)) ?? undefined;
  } catch {
    return undefined;
  }
}

function writeCompactDismissedAt(sessionId: string): void {
  try {
    localStorage.setItem(compactDismissedKey(sessionId), new Date().toISOString());
  } catch {
    // storage unavailable — the dismissal just lasts this mount
  }
}

export interface ConversationTabProps {
  sessionId: string;
  routerClient: RouterClient;
  /**
   * The Shell dock's transport. Defaults to a ShellBridge over the Electron
   * shell host (window.mads.shell) for this session; undefined outside
   * Electron (e.g. `npm run dev`'s renderer-only server), where the dock
   * degrades to a plain notice rather than importing node-pty (HANDOFF.md §5).
   * Passed explicitly only by the in-memory demo.
   */
  shellBridge?: ShellDockBridge;
  /** Shows the ProvisioningStrip until the session goes active — set for a session just created from the Launchpad. */
  provisioning?: boolean;
  onProvisioningDone?: () => void;
  /** A goal/message to auto-send through the Composer once, on mount — set for a session just created from the Launchpad. */
  pendingFirstMessage?: string;
  onPendingFirstMessageHandled?: () => void;
  /** Opens a file's diff in the Files tab — the Commit dialog's path links. */
  onOpenFile?: (path: string) => void;
  /** Opens one file's diff in a recorded commit in the Files tab — the auto-merged Land card's "See the changes". */
  onOpenCommitFile?: (hash: string, path: string) => void;
  /** Opens one of this session's delegates (by harness id) as a full-pane view — the dock's "Open as page". */
  onOpenDelegate?: (harnessId: string) => void;
  /** Opens one of this session's tasks in the Tasks tab — the composer's "/task" toast link. */
  onOpenTask?: (taskId: string) => void;
}

type DisplayMode = "chat" | "terminal";

// Chat and Terminal are two transports over one provider conversation, not
// simultaneous mirrors — a toggle click drives a real backend switch
// (POST /transport) with single-writer handoff, not just client-side state.
type TransportState =
  | { status: "idle"; mode: DisplayMode }
  | { status: "switching"; from: DisplayMode; to: DisplayMode }
  | {
      status: "failed";
      attemptedMode: DisplayMode;
      code: "busy" | "resume_failed" | "not_ready";
      retryable: boolean;
    };

// Derived, never stored: "live" while the keyed conversation says a turn
// runs (hello.turn, a user_message, the turn's first item; turn_end, an
// error frame or Stop end it), whoever started it. "revealing" is a turn the
// agent has finished whose paced reveal is still playing: the composer and
// working indicator are already idle (the agent is), while the turn's
// activity stays open until the reveal drains.
type TurnPhase = "idle" | "live" | "revealing";

/**
 * Top-level Conversation tab (MADS-UX §3.3 / W6.3). Composes the history (or
 * the live terminal), the inactive-session summary card, the session dock at
 * the bottom in both views (context rim, composer in chat, status line with
 * the persona/model/trust pickers, location and Commit/Land), and the docked
 * Shell — see HANDOFF.md §8 for the demo-mount rationale (no Session
 * Workspace shell exists yet to register this into).
 */
export function ConversationTab({
  sessionId,
  routerClient,
  shellBridge: shellBridgeProp,
  provisioning,
  onProvisioningDone,
  pendingFirstMessage,
  onPendingFirstMessageHandled,
  onOpenFile,
  onOpenCommitFile,
  onOpenDelegate,
  onOpenTask,
}: ConversationTabProps) {
  const [session, setSession] = useState<Session | null>(null);
  const [loadError, setLoadError] = useState(false);
  const [retryToken, setRetryToken] = useState(0);
  const [transport, setTransport] = useState<TransportState>({ status: "idle", mode: "chat" });
  // Per-session and outside React state, so hiding the dock and switching
  // sessions (which remounts this tab) don't lose it. Hide is not end: the
  // shell lives in the shell host, this only says whether it is showing.
  const { open: shellOpen, height: shellHeight } = useShellDockState(sessionId);
  const shellHost = typeof window !== "undefined" ? window.mads?.shell : undefined;
  const shellBridge = useMemo(
    () => shellBridgeProp ?? (shellHost ? new ShellBridge(shellHost, sessionId) : undefined),
    [shellBridgeProp, shellHost, sessionId],
  );
  // Terminal view's compose sheet (⌘E / Ctrl+Shift+E): the line-numbered
  // editor rising from the dock over the xterm, sent as a bracketed paste.
  const [composeOpen, setComposeOpen] = useState(false);
  const [composeText, setComposeText] = useState("");
  const terminalViewRef = useRef<TerminalViewHandle>(null);
  const [eventSource, setEventSource] = useState<SessionEventSource | null>(null);
  const [sendError, setSendError] = useState<string>();
  const [commandToast, setCommandToast] = useState<{
    tone: "ok" | "bad";
    message: string;
    action?: { label: string; onClick: () => void };
  }>();
  const [waking, setWaking] = useState(false);
  // Why the latest send failed (its HTTP error, or the stream's error
  // frame) — the reason on a send the stream reported unsaved.
  const [sendFailure, setSendFailure] = useState<string>();
  // session.workspace is a reference (id or name), not the name the dock's
  // ⎇ segment prints — resolved here, off the session this tab already holds,
  // and kept live by the hook's workspace.* subscription.
  const sessionWorkspace = useWorkspaceForRef(routerClient, session?.workspace);
  const agents = useAgentIndex(routerClient);
  const checkoutBranch = useCheckoutBranch(routerClient, session);
  // The reveal's reading pace in words per minute (Native by default).
  const wordsPerMinute = streamPaceToWpm(useStreamSpeed(routerClient));
  // Last completed turn's usage, for the dock's context rim and the + menu's
  // context row. Populated from the stream's turn_end frame (the only place
  // usage is reported), seeded from session.last_usage at mount.
  const [lastUsage, setLastUsage] = useState<Usage>();
  // Native compaction progress (Runner's resume-above-threshold trigger,
  // services/session/internal/runner/compact.go) — surfaced explicitly since
  // headless -p mode has no interactive "resume from summary" prompt.
  // Cleared on session switch same as everything else reset in the mount
  // effect below; not persisted, since it's meaningful only while this tab
  // is watching the live stream.
  const [compactStatus, setCompactStatus] = useState<CompactStatus>();
  // The in-flight turn's server-side start (Session.agent_state.since, then
  // live session.agent_state events) — the working indicator's elapsed
  // anchor, so a remount mid-turn doesn't restart the counter.
  const [agentSince, setAgentSince] = useState<string>();
  // The event-log replay's compaction entry, parked until the session record
  // is also in: turning it (and replayed tasks) into live state needs both,
  // and the two fetches race. null once applied for this mount.
  const [pendingReplay, setPendingReplay] = useState<
    { compact: ConversationHistory["compact"] } | null
  >(null);
  // Interactive permission prompts (chat transport, TrustLimited/
  // TrustReadOnly — see providers/claudecode/README.md "Trust enforcement").
  // Keyed by request id so a live "permission_request" stream event
  // (upserted below) and the mount-time GET /permission-requests
  // reconstruction (below) never duplicate the same card, whichever arrives
  // first.
  const [permissionRequests, setPermissionRequests] = useState<
    Map<string, PermissionRequest>
  >(new Map());
  const upsertPermissionRequest = useCallback((req: PermissionRequest) => {
    setPermissionRequests((m) => new Map(m).set(req.id, req));
  }, []);
  // A mount against a dormant session opens /output before any child
  // exists; the Router 404s and the browser's EventSource, per spec, never
  // auto-reconnects a connection whose *initial* handshake failed (only a
  // previously-established one retries on a later drop). streamBroken flags
  // exactly that dead-on-arrival case; once the session is confirmed active
  // again (session.activity_changed, picked up by the
  // WORKSPACE_SESSION_EVENT_TYPES subscription below) the streams reopen —
  // the chat's keyed one from its last frame (reattach), the terminal's by
  // streamGeneration. Not a timer/backoff loop, just a single deliberate
  // re-open keyed to a real state transition.
  const [streamBroken, setStreamBroken] = useState(false);
  const [streamGeneration, setStreamGeneration] = useState(0);
  const links = useSessionLinks([sessionId]);
  const linkName = useLinkNames(routerClient, sessionId, links);
  // A follower started without a goal waits on its leader's first message.
  const leaderId = leaderLink(links, sessionId)?.granting_session_id;
  const linkNameRef = useRef(linkName);
  linkNameRef.current = linkName;

  // --- Commit and Land (docs/SESSION-WORKTREES-DESIGN.md D4–D6) ---
  // Result cards from the event log plus the actions that make them; the
  // dock's Commit/Land control reads its state off the session record's counts.
  const worktree = useWorktreeCards(routerClient, sessionId);
  const { commitAll, land } = worktree;
  const loadWorktreeStatus = useCallback(() => routerClient.getWorktreeStatus(sessionId), [routerClient, sessionId]);
  const commitSettings = useCommitSettings(routerClient);
  // The Commit dialog, optionally opened on a one-click Commit's refusal
  // (no Fast Model after all, a failing hook) so the user can see why and
  // write the message themselves.
  const [commitDialog, setCommitDialog] = useState<{ initialError?: CommitError } | null>(null);
  // Stop asked while subagents or processes run — what it would end. Unset: no dialog.
  const [stopConfirm, setStopConfirm] = useState<{ subagents: number; processes: number }>();
  // "Ask the agent to resolve" drafts into the composer; it never sends.
  const [composerPrefill, setComposerPrefill] = useState<{ text: string; nonce: number }>();

  // --- Conversation-redesign state (T12–T17) ---
  // Out-of-band tool status changes (pkg/toolstatus synthesis:
  // interrupted/pending/in_progress), merged by tool_use id — live from the
  // session.tool_status_changed control-plane stream, historical from the
  // event-log replay in the mount effect below.
  const [toolStatuses, setToolStatuses] = useState<Map<string, ToolStatus>>(new Map());
  // Task subagent lifecycles by task_id (live "task" stream frames +
  // replayed session.subagent_task envelopes).
  const [tasks, setTasks] = useState<Map<string, TaskModel>>(new Map());
  // Running background processes that aren't subagents (a run_in_background
  // Bash call, a Monitor watch): the live "background" frame, whole on each
  // change and first on connect. Never persisted — a fresh stream starts
  // from what it's told.
  const [background, setBackground] = useState<BackgroundTask[]>([]);
  // The server's delegate records for this session (docs/DELEGATES-DESIGN.md),
  // by harness id — laid over `tasks` at render (mergeDelegates) as the one
  // authority on whether each subagent is running and how it ended.
  const [delegates, setDelegates] = useState<Map<string, Delegate>>(new Map());
  // Subagent streams read back from the event log (session.subagent_event),
  // by spawning Agent tool_use id — only for a dock whose subagent has no
  // transcript file and no live rows (the keyed stream's subagentRows,
  // which never enter the main list).
  const [subagentReplay, setSubagentReplay] = useState<Map<string, SubStreamItem[]>>(new Map());
  // The V2 working indicator's current-activity ticker: latest tool title,
  // or "Thinking" fed by thinking_tokens ticks.
  const [currentActivity, setCurrentActivity] = useState<string>();
  // Bottom-dock target — one tool's full detail or one subagent's stream.
  // Stored as ids so the open dock keeps tracking live updates; resolved to
  // a fresh DockView at render time below.
  const [dockTarget, setDockTarget] = useState<
    | { type: "tool"; toolId?: string; tool: ToolUse }
    | { type: "subagent"; parentToolUseId: string; title: string; subtype?: string }
    | null
  >(null);

  useEffect(() => {
    // Ctrl+` hides or shows; it never ends the shell. Showing a dock with no
    // shell behind it is what starts one (TerminalView's mount opens it).
    const handler = () => {
      const next = !getShellDockState(sessionId).open;
      // Two bottom surfaces never stack: opening the Shell dock closes
      // the activity dock (and vice versa — see the open handlers below).
      if (next) setDockTarget(null);
      setShellDockState(sessionId, { open: next });
    };
    window.addEventListener(SHELL_TOGGLE_EVENT, handler);
    return () => window.removeEventListener(SHELL_TOGGLE_EVENT, handler);
  }, [sessionId]);

  // A successful "Conversation compacted." is a transient confirmation, not
  // standing state — auto-dismiss it (it used to sit until session switch).
  // A failed pass stays until the user dismisses it via the banner's ×.
  useEffect(() => {
    if (compactStatus?.phase !== "done" || !compactStatus.success) return;
    const id = window.setTimeout(() => setCompactStatus(undefined), 6000);
    return () => window.clearTimeout(id);
  }, [compactStatus]);

  // The keyed conversation (docs/CHAT-STREAM-DESIGN.md §5, §6): the last
  // transcript page plus /output?v=2, merged by key — the only source of
  // the rows, the turn state and the queue's snapshot. The terminal view
  // reads transcript pages only (its PTY rides the legacy stream).
  const ptyMode =
    transport.status === "idle"
      ? transport.mode === "terminal"
      : transport.status === "switching" && transport.to === "terminal";
  const statusRef = useRef<(kind: string, data: Record<string, unknown>) => void>(() => {});
  const conv = useKeyedConversation(routerClient, sessionId, {
    stream: !ptyMode,
    onStatus: (kind, data) => statusRef.current(kind, data),
    onLoadError: () => setLoadError(true),
    onDisconnect: () => setStreamBroken(true),
  });
  const { dispatch: convDispatch, reattach: convReattach, refreshLast } = conv;
  const turnLive = conv.state.turn.active;
  const ptyModeRef = useRef(ptyMode);
  ptyModeRef.current = ptyMode;
  // The user's messages waiting behind the in-flight turn. The session
  // process owns the queue (POST /messages with queue:true), so it survives
  // this pane unmounting; this is only its mirror — hello's snapshot, then
  // kept current by session.queue_changed.
  const queuedMessages = conv.state.queued;
  // Paced reveal of the turn's text; `final` once the turn is over, so the
  // last word shows whole.
  const reveal = useReveal(conv.rows, wordsPerMinute, !turnLive);
  const showAll = usePresence(reveal.behind, FADE_OUT_MS);
  const turnPhase: TurnPhase = turnLive ? "live" : reveal.pending ? "revealing" : "idle";
  // Latest turn phase, for callbacks that fire outside the render that set it.
  const turnPhaseRef = useRef(turnPhase);
  turnPhaseRef.current = turnPhase;
  // A turn starting while the last reply still reveals shows the rest of it
  // at once, so the new message lands after the whole reply. Only the last
  // turn's rows: a turn opened by its own first text paces that text.
  const wasLive = useRef(turnLive);
  const { revealAll } = reveal;
  useEffect(() => {
    if (turnLive && !wasLive.current) revealAll((r) => !r.current);
    wasLive.current = turnLive;
  }, [turnLive, revealAll]);

  useEffect(() => {
    let cancelled = false;
    setLoadError(false);
    setSession(null);
    setPermissionRequests(new Map());
    setLastUsage(undefined);
    setCompactStatus(undefined);
    setAgentSince(undefined);
    setPendingReplay(null);
    setToolStatuses(new Map());
    setTasks(new Map());
    setBackground([]);
    setDelegates(new Map());
    setSubagentReplay(new Map());
    setSendFailure(undefined);
    setCurrentActivity(undefined);
    setDockTarget(null);
    // Reset to the chat default up front — the mode lookup below is async,
    // so a stale mode carried over from whatever session was mounted before
    // this one must never leak into this session's initial render.
    setTransport({ status: "idle", mode: "chat" });
    void routerClient
      .getSession(sessionId)
      .then((s) => {
        if (!cancelled) {
          setSession(s);
          // Seeds the context rim from the session record's last-known
          // usage at wake time — before this mount has seen a turn_end of
          // its own (services/session/internal/runner's persistUsage).
          if (s.last_usage) setLastUsage(s.last_usage);
          // Reopened mid-turn: the stream's hello says whether a turn runs;
          // the record's mirrored agent state only seeds the indicator's
          // label. Only a live Runner clears the mirror, so an inactive
          // session's "working" is a stale leftover.
          if (s.activity === "active" && s.agent_state?.state === "working") {
            setAgentSince(s.agent_state.since);
            if (s.agent_state.label) setCurrentActivity((a) => a ?? s.agent_state?.label);
          }
        }
      })
      .catch(() => {
        if (!cancelled) setLoadError(true);
      });
    // The transcript's last page is the keyed conversation's own mount
    // fetch; a Retry asks for it again.
    if (retryToken > 0) {
      refreshLast().catch(() => {
        if (!cancelled) setLoadError(true);
      });
    }
    // The session service's current transport mode IS the persistence
    // mechanism for the toggle's last state — every switch is a real
    // SwitchTransport, so there's nothing else to read the "last choice"
    // from. A single read on mount, never a switch: a dormant session with
    // no live child (and so no transport history) answers "chat", which is
    // already this effect's default above, so a failed/degenerate lookup
    // just leaves that default standing. The mode is validated rather than
    // trusted blindly, so an unexpected response shape (e.g. a route that
    // doesn't exist yet against a plain test fetch mock) degrades to that
    // same default instead of forcing the tab into a broken non-chat state.
    // Reconstructs any still-open card (acceptance: chat rendering of
    // permission events survives a transcript refetch/remount, not just a
    // live stream still being connected) — a resolved request's terminal
    // event is already carried by the live stream, so nothing further is
    // needed for those once this mount is done.
    void routerClient
      .getPermissionRequests(sessionId)
      .then((reqs) => {
        if (cancelled) return;
        setPermissionRequests(new Map(reqs.map((r) => [r.id, r])));
      })
      .catch(() => {});
    // History parity (T17): the control-plane event log is the only
    // historical source for task lifecycles, compaction and out-of-band
    // status changes — the provider transcript omits them. Merge live-wins:
    // anything the stream delivered while this fetch was in flight is
    // fresher than the replay.
    void getAllEvents(routerClient, { sessionId, types: CONVERSATION_EVENT_TYPES })
      .then((envelopes) => {
        if (cancelled) return;
        const h = foldHistoryEvents(envelopes);
        setToolStatuses((live) => new Map([...h.statuses, ...live]));
        setTasks((live) => new Map([...h.tasks, ...live]));
        setPendingReplay({ compact: h.compact });
      })
      .catch(() => {});
    // Live-wins, like the event replay: a delegate_changed that landed while
    // this was in flight is newer than the snapshot.
    void routerClient
      .getSessionDelegates(sessionId)
      .then((list) => {
        if (cancelled) return;
        setDelegates((live) => new Map([...list.map((d) => [d.harness_id, d] as const), ...live]));
      })
      .catch(() => {});
    void routerClient
      .getTransportMode(sessionId)
      .then(({ mode }) => {
        if (!cancelled && (mode === "chat" || mode === "terminal")) {
          setTransport({ status: "idle", mode });
        }
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [routerClient, sessionId, retryToken, refreshLast]);

  // Finishes the event-log replay once the session record is in too (see
  // pendingReplay): subagents spawned inside a turn still in flight are
  // running again, and a compaction still running — or failed and not yet
  // dismissed — gets its banner back. Live state that landed meanwhile wins.
  useEffect(() => {
    if (!session || !pendingReplay) return;
    const active = session.activity === "active";
    if (active && session.agent_state?.state === "working") {
      const since = session.agent_state.since;
      setTasks((m) => resumeRunningTasks(m, since));
    }
    const replayed = replayedCompactStatus(
      pendingReplay.compact,
      active,
      readCompactDismissedAt(sessionId),
    );
    if (replayed) setCompactStatus((cur) => cur ?? replayed);
    setPendingReplay(null);
  }, [session, pendingReplay, sessionId]);

  // The indicator's anchor belongs to one turn — drop it once the turn is
  // over, so the next send doesn't count from the previous turn's start
  // until its own session.agent_state arrives.
  useEffect(() => {
    if (turnPhase !== "live") setAgentSince(undefined);
  }, [turnPhase]);

  const refetchSession = useCallback(() => {
    void routerClient.getSession(sessionId).then(setSession);
  }, [routerClient, sessionId]);

  // Persists a model change and reconfigures the running agent
  // (POST /sessions/{id}/model). Resolves the child's queued flag: true means
  // a turn was in flight and the switch lands at the end of it — the agent is
  // never interrupted to change models. Throws on failure so both callers can
  // say so; the record is already saved by the time the Router reports a
  // child failure, hence the Router's own "saved, but..." wording.
  const applyModel = useCallback(
    async (model: string): Promise<boolean> => {
      const { session: updated, queued } = await routerClient.setSessionModel(sessionId, model);
      setSession((s) => (s ? { ...s, model: updated.model } : s));
      return queued;
    },
    [routerClient, sessionId],
  );

  // The dock's model picker. The composer's "/model" goes through
  // handleUpdateSession instead, which reuses applyModel and lets the
  // composer render the confirmation with the model's display name.
  const handleChangeModel = useCallback(
    (model: string) => {
      void applyModel(model)
        .then((queued) => {
          setCommandToast({
            tone: "ok",
            message: queued
              ? `Model set to ${model} — applies when the current turn finishes.`
              : `Model set to ${model}.`,
          });
        })
        .catch((err) => {
          setCommandToast({
            tone: "bad",
            message: err instanceof Error ? err.message : "Failed to change model.",
          });
        });
    },
    [applyModel],
  );

  // The composer's "/model". There is deliberately no persona path: a
  // persona's prompt and tools are spawn-time, and with no Router endpoint to
  // bounce the child a picker would only relabel the screen.
  const handleUpdateSession = useCallback(
    async (patch: Partial<Pick<Session, "model">>) => {
      if (patch.model !== undefined) return applyModel(patch.model);
    },
    [applyModel],
  );

  // The dock's trust picker → POST /sessions/{id}/trust. Unlike the
  // local-only persona above this one persists (and the Router bounces a live child so
  // the new level actually applies — trust is spawn-time-only in the
  // runner). The confirmation is the inline "Trust changed: …" notice the
  // Router's session.trust_changed draws in the history (useWorktreeCards),
  // which says whether the agent restarted and survives a reload — so a
  // success adds no toast of its own. A failure still does: a bounce that
  // failed is a 502 whose message says the level was saved anyway.
  const handleChangeTrust = useCallback(
    (trust: NonNullable<Session["trust"]>) => {
      void routerClient
        .setSessionTrust(sessionId, trust)
        .then((updated) => {
          // effective_trust rides along: the Router recomputes it for the
          // new level (auto_review on a provider with no reviewer → limited),
          // and a stale one would keep — or miss — the dock's fallback.
          setSession((s) =>
            s ? { ...s, trust: updated.trust, effective_trust: updated.effective_trust } : s,
          );
        })
        .catch((err) => {
          setCommandToast({
            tone: "bad",
            message: err instanceof Error ? err.message : "Failed to change trust.",
          });
        });
    },
    [routerClient, sessionId],
  );

  // Conversation scroll: auto-follow the tail as the agent streams, but yield
  // control the instant the user scrolls up — re-engaging only once they're
  // back at the bottom (useStickToBottom). The chat stream mounts and
  // unmounts with the transport, so its ref is the hook's callback ref; the
  // terminal stream just takes the element ref.
  const stick = useStickToBottom();
  // Scrolling near the top loads the page before the oldest row, keeping
  // the item being read where it was.
  const olderPages = useOlderPages(stick.element, {
    hasMore: conv.state.hasMore,
    loadOlder: conv.loadOlder,
    version: `${conv.state.cursor}/${conv.state.hasMore}`,
  });
  // The working indicator a send showed before its turn was confirmed: a
  // send that fails takes it back.
  const optimisticTurn = useRef(false);

  // The dock's Commit…: one click with "Write commit messages for me" on and a
  // Fast Model to write them, else the dialog.
  const handleCommit = useCallback(() => {
    if (!(commitSettings.autoCommitMessages && commitSettings.fastModel)) {
      setCommitDialog({});
      return;
    }
    // The result is a card at the tail; follow it there (see the scroll
    // lock above), as a sent message does.
    stick.follow();
    commitAll().catch((err: unknown) => {
      if (err instanceof CommitRefusedError) setCommitDialog({ initialError: err.body });
      else
        setCommandToast({
          tone: "bad",
          message: err instanceof Error ? err.message : "Couldn't commit.",
        });
    });
  }, [commitSettings, commitAll, stick]);

  // The dock's Land / a card's "Land again". The outcome is a card either
  // way (and the dock's ⎇ popover shows it at the button); only an unexpected
  // failure (no card coming) is a toast. The user asked for this outcome, so
  // the tail follows it into view even if they had scrolled up.
  const handleLand = useCallback(() => {
    stick.follow();
    land().catch((err: unknown) => {
      setCommandToast({
        tone: "bad",
        message: err instanceof Error ? err.message : "Couldn't land.",
      });
    });
  }, [land, stick]);

  const worktreeActions = useMemo(
    () => ({
      onUndo: worktree.undo,
      onAskAgent: (text: string) => setComposerPrefill((p) => ({ text, nonce: (p?.nonce ?? 0) + 1 })),
      onLandAgain: handleLand,
      landing: worktree.landing,
      onOpenCommitFile,
    }),
    [worktree.undo, worktree.landing, handleLand, onOpenCommitFile],
  );

  // Shares SessionWorkspace's session-scoped subscription (see
  // WORKSPACE_SESSION_EVENT_TYPES) rather than opening its own. A
  // linked-session inject/digest needs nothing here: it reaches the
  // conversation as a user_message frame like any send.
  useEffect(() => {
    return subscribeToEvents(
      { sessionId, types: WORKSPACE_SESSION_EVENT_TYPES },
      (envelope) => {
        if ((SESSION_SHELL_REFETCH_EVENT_TYPES as string[]).includes(envelope.type)) {
          refetchSession();
        }
        // Anything else in WORKSPACE_SESSION_EVENT_TYPES (task.*/stage.*/
        // file.changed/note.created/pipeline.*) is there only for other
        // subscribers sharing this bus (usePipelineRun) — this tab has
        // nothing to do with them.
      },
    );
  }, [refetchSession, sessionId]);

  // The terminal view's data-plane stream (services/session's legacy GET
  // /output, proxied via the Router): the channel SessionTerminalBridge
  // rides for pty_output. The chat view never opens it — its conversation
  // and status frames come from the keyed stream above. A switch to
  // terminal opens it as the switch starts; the PTY's buffer replays on
  // attach.
  useEffect(() => {
    if (!ptyMode) return;
    const source = routerClient.streamSession(sessionId);
    setEventSource(source);
    setStreamBroken(false);
    const onError = (ev: MessageEvent<string>) => {
      // A transport failure (no data): dead on arrival against a dormant session.
      if (typeof ev.data !== "string") setStreamBroken(true);
    };
    source.addEventListener("error", onError);
    return () => {
      source.removeEventListener("error", onError);
      source.close();
      setEventSource(null);
    };
  }, [routerClient, sessionId, streamGeneration, ptyMode]);

  // Reopens both streams: the keyed one from the last frame it saw.
  const reattachStreams = useCallback(() => {
    setStreamBroken(false);
    setStreamGeneration((g) => g + 1);
    convReattach();
  }, [convReattach]);

  // Reconnects exactly once a dead-on-arrival connection (streamBroken)
  // coincides with the session having gone active — see the
  // streamBroken/streamGeneration declaration above.
  useEffect(() => {
    if (streamBroken && session?.activity === "active") reattachStreams();
  }, [streamBroken, session?.activity, reattachStreams]);

  // Forced reattach for "Restart agent" (SessionWorkspace's kebab) — fires
  // unconditionally on a successful bounce, regardless of the cached
  // session.activity value, unlike the passive reconnect above. See
  // streamReattachSignal.ts for why this can't just wait on that path.
  useEffect(() => {
    const handler = (e: Event) => {
      const detail = (e as CustomEvent<StreamReattachDetail>).detail;
      if (detail?.sessionId !== sessionId) return;
      reattachStreams();
    };
    window.addEventListener(STREAM_REATTACH_EVENT, handler);
    return () => window.removeEventListener(STREAM_REATTACH_EVENT, handler);
  }, [sessionId, reattachStreams]);

  const terminalBridge = useMemo(
    () =>
      eventSource ? new SessionTerminalBridge(routerClient, sessionId, eventSource) : undefined,
    [routerClient, sessionId, eventSource],
  );

  const handleToggle = useCallback(
    async (target: DisplayMode) => {
      if (transport.status === "switching") return; // ignore re-clicks mid-switch
      const from = transport.status === "idle" ? transport.mode : transport.attemptedMode;
      if (transport.status === "idle" && transport.mode === target) return;
      setTransport({ status: "switching", from, to: target });
      try {
        await routerClient.switchTransport(sessionId, target);
        setTransport({ status: "idle", mode: target });
      } catch (e) {
        if (e instanceof TransportSwitchError) {
          setTransport({
            status: "failed",
            attemptedMode: target,
            code: e.code,
            retryable: e.retryable,
          });
        } else {
          throw e;
        }
      }
    },
    [routerClient, sessionId, transport],
  );

  const handleInterruptAndSwitch = useCallback(
    async (target: DisplayMode) => {
      try {
        await routerClient.interrupt(sessionId);
      } catch {
        // best-effort — the switch attempt below surfaces any real problem
      }
      await handleToggle(target);
    },
    [routerClient, sessionId, handleToggle],
  );

  // Ends the turn here: the agent is done (turn_end, an error frame, Stop, or
  // the control plane's idle), so the composer and indicator go idle now. A
  // paced reveal keeps playing ("revealing") over the keyed rows; nothing is
  // refetched or replaced — the keyed conversation refreshes its last page
  // once at turn_end, merged by key.
  const endTurn = useCallback(() => {
    setCurrentActivity(undefined);
    // A turn ending with no task_notification means those subagents never
    // finished — interrupted-shape, never a spinner that ticks forever.
    setTasks((m) => finishRunningTasks(m));
    // A Stop question outlives nothing: the turn it asked about is over.
    setStopConfirm(undefined);
    convDispatch({ type: "turn", turn: { active: false } });
  }, [convDispatch]);

  // The chat-mode "Stop" button — the ⌘/Ctrl-C of the terminal. Interrupts the
  // in-flight turn, then finalizes locally rather than waiting on the child's
  // turn_end frame, so the stop reads as immediate. The child still sends its
  // own turn_end; that just lands on an already-idle turn.
  const handleInterrupt = useCallback(async () => {
    try {
      await routerClient.interrupt(sessionId);
    } catch (err) {
      setSendError(err instanceof Error ? err.message : "Failed to stop the agent.");
      return;
    }
    endTurn();
  }, [routerClient, sessionId, endTurn]);

  // The keyed stream's status frames (today's provider.Event data): the
  // conversation itself tracks turn_end, the rest is this tab's own state.
  statusRef.current = (kind, data) => {
    if (kind === "hello") {
      // A connection that didn't resume starts from its own snapshots (sent
      // only when there is something to say): the last one's processes are
      // gone with it.
      if (data.resume !== "resumed") setBackground([]);
      return;
    }
    const ev = data as unknown as StreamEvent;
    switch (ev.kind) {
      case "turn_end":
        // turn_end carries this turn's usage — the only place it's reported,
        // so it feeds the dock's context rim. One that counts no tokens (a
        // /compact turn the CLI gave no figure for) leaves the rim where it was.
        if (ev.usage && countsTokens(ev.usage)) setLastUsage(ev.usage);
        endTurn();
        return;
      case "error":
        endTurn();
        setSendError(ev.text || "The agent hit an error.");
        setSendFailure(ev.text || undefined);
        return;
      case "permission_request":
        upsertPermissionRequest(ev.permission);
        return;
      case "task":
        // Not a turn signal: lifecycle lines arrive between turns too.
        setTasks((m) => applyTaskEvent(m, ev.task, true));
        return;
      case "background":
        setBackground(ev.background ?? []);
        return;
      case "compact":
        setCompactStatus(ev.compact);
        return;
      case "thinking":
        // The thinking-token tick: just the indicator's ticker. (Thinking
        // text is a keyed row.) claude-code streams a subagent's ticks
        // untagged, so a tick never opens a turn.
        setCurrentActivity(
          ev.thinking_tokens ? `Thinking · ${formatTokens(ev.thinking_tokens)} tokens` : "Thinking…",
        );
        return;
      case "inbound":
        // A message from outside MADS with no key to merge by: it shows once
        // stored (the turn-end page); its turn starts here.
        if (!ev.parent_tool_use_id && !conv.state.turn.active) convDispatch({ type: "turn", turn: { active: true } });
        return;
    }
  };

  // The ticker names what the turn's latest activity row is doing: its
  // tool, or thinking. Status ticks refine it in between.
  let activityRow: (typeof conv.rows)[number] | undefined;
  for (let i = conv.rows.length - 1; i >= 0 && !activityRow; i--) {
    const r = conv.rows[i];
    if (r.current && (r.role === "tool" || r.role === "thinking")) activityRow = r;
  }
  const activityKey = activityRow?.key;
  const activityLabel = activityRow
    ? activityRow.role === "tool" && activityRow.tool
      ? toolTitle(activityRow.tool, linkName)
      : "Thinking…"
    : undefined;
  useEffect(() => {
    if (turnLive && activityKey && activityLabel) setCurrentActivity(activityLabel);
  }, [turnLive, activityKey, activityLabel]);

  // Durable turn-completion reconciliation: the runner's session.turn_ended
  // envelope rides the control-plane event log, so it closes a turn the
  // stream's own turn_end didn't reach (a stream down meanwhile). Idempotent
  // when both signals arrive. In the terminal view, which reads transcript
  // pages only, it is also when the last page is fetched again.
  //
  // session.agent_state rides the same log: "working" marks a turn in flight
  // even when this tab saw none of its frames (no stream connected) and
  // carries the turn's start for the elapsed counter; "idle" closes the turn
  // exactly as turn_ended does — and unlike turn_ended, it fires for a turn
  // with no digest delta too.
  // Delegate records change on the control plane — a backgrounded subagent
  // finishes long after the turn (and the /output stream's frames) that
  // spawned it, so this is the signal that ends its spinner.
  useEffect(() => {
    return subscribeToEvents({ sessionId, types: ["session.delegate_changed"] }, (envelope) => {
      const d = envelope.payload as Delegate | undefined;
      if (!d?.harness_id) return;
      setDelegates((m) => new Map(m).set(d.harness_id, d));
    });
  }, [sessionId]);

  useEffect(() => {
    return subscribeToEvents({ sessionId, types: ["session.turn_ended", "session.agent_state"] }, (envelope) => {
      if (envelope.type === "session.agent_state") {
        const state = envelope.payload as Partial<AgentState> | undefined;
        if (state?.state === "working") {
          // Only from idle. The control plane can deliver a turn's
          // "working" after the stream already ended that turn, so while a
          // reply is revealing it is no proof of a new turn — the stream's
          // own next frame is, being ordered.
          if (turnPhaseRef.current === "idle") {
            convDispatch({ type: "turn", turn: { active: true, startedAt: state.since || envelope.timestamp } });
          }
          setAgentSince(state.since || envelope.timestamp);
          // The stream's own tool/thinking frames keep the ticker current;
          // the server label only fills it when none has arrived yet.
          if (state.label) setCurrentActivity((a) => a ?? state.label);
          return;
        }
        if (state?.state !== "idle") return;
      }
      if (ptyModeRef.current) refreshLast().catch(() => {});
      if (turnPhaseRef.current === "live") endTurn();
    });
  }, [sessionId, convDispatch, endTurn, refreshLast]);

  // Queue changes. A queued message going out needs nothing here: it
  // reaches the conversation as a user_message frame, which opens its turn.
  useEffect(() => {
    return subscribeToEvents({ sessionId, types: ["session.queue_changed"] }, (envelope) => {
      const change = envelope.payload as Partial<QueueChanged> | undefined;
      if (!change) return;
      convDispatch({ type: "queued", queued: change.queued ?? [] });
      if (change.failed) {
        setSendError(`Couldn't send your queued message: ${change.error ?? "unknown error"}`);
      }
    });
  }, [sessionId, convDispatch]);

  // Queued messages live in the session process, so a session that stops
  // has none, whatever the last event said.
  useEffect(() => {
    if (session && session.activity !== "active") convDispatch({ type: "queued", queued: [] });
  }, [session, convDispatch]);

  const handleCancelQueued = useCallback(
    (queuedId: string) => {
      convDispatch({ type: "queued", queued: queuedMessages.filter((m) => m.id !== queuedId) });
      routerClient.cancelQueuedMessage(sessionId, queuedId).catch((err: unknown) => {
        setSendError(err instanceof Error ? err.message : "Couldn't remove the queued message.");
      });
    },
    [routerClient, sessionId, convDispatch, queuedMessages],
  );

  const handleCompactNow = useCallback(async (instructions?: string) => {
    // Optimistic "Compacting…" so the click lands visibly even before the
    // provider's own compact/start stream event arrives (the Router may
    // first have to wake a dormant session); the real done/failed event
    // overwrites it.
    setCompactStatus({ phase: "start" });
    try {
      await routerClient.compactSession(sessionId, instructions);
    } catch (err) {
      setCompactStatus(undefined);
      setSendError(err instanceof Error ? err.message : "Failed to compact the conversation.");
    }
  }, [routerClient, sessionId]);
  // Compaction is never emulated: a provider whose matrix says it can't
  // compact gets no Compact affordance at all (the + menu item and the
  // context rim's link both hide when onCompact is absent).
  const compaction = useCompactionSupport(session?.provider);
  // Out-of-band status synthesis (session.tool_status_changed) rides the
  // control-plane /events stream, not the data-plane /output — subscribe
  // alongside the WORKSPACE_SESSION_EVENT_TYPES subscription above.
  useEffect(() => {
    return subscribeToEvents(
      { sessionId, types: ["session.tool_status_changed"] },
      (envelope) => {
        const p = envelope.payload as ToolStatusChange | undefined;
        if (!p?.tool_use_id || !p.status) return;
        setToolStatuses((m) => new Map(m).set(p.tool_use_id, p.status));
      },
    );
  }, [sessionId]);

  // The dock's Commit/Land counts are only recounted at turn ends and by
  // mads's own Commit/Land, so work committed or merged outside mads would
  // leave them offering what the dialog then finds nothing for. Recount for
  // this session — only the one on screen — when it opens and when the
  // window regains focus; the new counts arrive as session.updated. One at
  // a time: a focus while one runs has nothing newer to see.
  useEffect(() => {
    let running = false;
    const recount = () => {
      if (running) return;
      running = true;
      routerClient
        .refreshWorktreeCounts(sessionId)
        .catch(() => {}) // best-effort: the next turn end recounts anyway
        .finally(() => {
          running = false;
        });
    };
    recount();
    window.addEventListener("focus", recount);
    return () => window.removeEventListener("focus", recount);
  }, [routerClient, sessionId]);

  // The conversation stream's element (see the scroll lock above).
  const streamRef = stick.element;
  // The approval jump's reach: the whole tab (stream + dock) for finding
  // cards, the pinned strip for what covers the stream's bottom edge. The reveal is
  // its latest click, published to collapsed groups (permissionReveal.ts).
  const tabRef = useRef<HTMLDivElement>(null);
  const pinnedRef = useRef<HTMLDivElement>(null);
  const [permissionReveal, setPermissionReveal] = useState<PermissionReveal | null>(null);
  // Same reach for the substrip: which task cards are already on screen.
  const visibleTaskCards = useVisibleCards(tabRef, streamRef, pinnedRef, {
    selector: "[data-task-id]",
    idAttribute: "data-task-id",
  });
  // ...and which background processes' rows are.
  const visibleBackgroundRows = useVisibleCards(tabRef, streamRef, pinnedRef, {
    selector: "[data-background-id]",
    idAttribute: "data-background-id",
  });
  const backgroundByTool = useMemo(
    () => new Map(background.filter((b) => b.tool_use_id).map((b) => [b.tool_use_id!, b])),
    [background],
  );
  const revealPermission = useCallback((id: string) => {
    // Jumping up the history is the user leaving the tail — don't let the
    // next streamed frame snap back before the smooth scroll's first event.
    stick.release();
    setPermissionReveal((cur) => ({ id, nonce: (cur?.nonce ?? 0) + 1 }));
  }, [stick]);

  // Ctrl/Cmd+↑/↓ hops between the user's own messages — anchored on the
  // [data-user-msg] elements ChatRenderer stamps on each user bubble.
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (!(e.ctrlKey || e.metaKey) || (e.key !== "ArrowUp" && e.key !== "ArrowDown")) return;
      const el = streamRef.current;
      if (!el) return;
      const anchors = Array.from(el.querySelectorAll<HTMLElement>("[data-user-msg]"));
      if (anchors.length === 0) return;
      e.preventDefault();
      const containerTop = el.getBoundingClientRect().top;
      const offsets = anchors.map((a) => a.getBoundingClientRect().top - containerTop + el.scrollTop);
      const cur = el.scrollTop;
      const target =
        e.key === "ArrowUp"
          ? ([...offsets].reverse().find((o) => o < cur - 4) ?? 0)
          : (offsets.find((o) => o > cur + 4) ?? el.scrollHeight);
      // Hopping up leaves the tail — as the approval jump does, let go now
      // rather than at the smooth scroll's first event.
      if (e.key === "ArrowUp") stick.release();
      el.scrollTo({ top: target, behavior: "smooth" });
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [stick, streamRef]);

  // The subagent dock's stream: the subagent's own transcript file, read on
  // open and again as it runs (its task id is its harness id — the file's
  // name). Live frames for it are the cue to re-read.
  const dockParent = dockTarget?.type === "subagent" ? dockTarget.parentToolUseId : undefined;
  const dockModel = dockParent
    ? [...mergeDelegates(tasks, delegates).values()].find((t) => t.toolUseId === dockParent)
    : undefined;
  const dockLive = useMemo(
    () => (dockParent ? rowsSubStream(subagentRows(conv.state, dockParent)) : undefined),
    [conv.state, dockParent],
  );
  const dockFile = useDelegateFile(
    routerClient,
    sessionId,
    dockModel?.taskId,
    `${dockLive?.length ?? 0}/${dockModel?.state ?? ""}/${dockModel?.running ?? ""}/${dockModel?.toolUses ?? ""}`,
  );
  const dockFileItems = useMemo(
    () => (dockFile?.available ? transcriptSubStream(dockFile.messages) : undefined),
    [dockFile],
  );
  // No file for it: read back what mads recorded live — every subagent's
  // stream, so only now, once per conversation. Live rows win for any
  // subagent the keyed stream still holds.
  const needStreamReplay = dockFile?.available === false;
  const streamReplayedFor = useRef<string>(undefined);
  useEffect(() => {
    if (!needStreamReplay || streamReplayedFor.current === sessionId) return;
    let cancelled = false;
    getAllEvents(routerClient, { sessionId, types: SUBAGENT_STREAM_EVENT_TYPES })
      .then((envelopes) => {
        if (cancelled) return;
        streamReplayedFor.current = sessionId;
        setSubagentReplay(foldHistoryEvents(envelopes).subagentStreams);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [needStreamReplay, routerClient, sessionId]);

  const showHistory = transport.status !== "idle" || transport.mode === "chat";

  // Find in conversation (⌘F / Ctrl+F): the chat view only — the terminal
  // view keeps Ctrl+F for readline. A selection in the tab seeds the query.
  const find = useFindBar();
  const { show: showFind, go: stepFind, close: closeFind, open: findOpen } = find;
  useEffect(() => {
    if (!showHistory) return;
    function onKey(e: KeyboardEvent) {
      // A key something else already handled (an editor's own find) isn't ours.
      if (e.defaultPrevented) return;
      const el = streamRef.current;
      // Hidden behind another workspace tab: not ours to take.
      if (!el || (el.checkVisibility && !el.checkVisibility())) return;
      if (matches("find", e)) {
        e.preventDefault();
        const sel = window.getSelection();
        const picked =
          sel && !sel.isCollapsed && sel.anchorNode && tabRef.current?.contains(sel.anchorNode)
            ? sel.toString().trim()
            : "";
        showFind(picked && !picked.includes("\n") ? picked : undefined);
      } else if (findOpen && (matches("findNext", e) || matches("findPrevious", e))) {
        e.preventDefault();
        stepFind(matches("findPrevious", e) ? -1 : 1);
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [showHistory, findOpen, showFind, stepFind, streamRef]);
  // The terminal view has no find; leaving chat closes it.
  useEffect(() => {
    if (!showHistory && findOpen) closeFind();
  }, [showHistory, findOpen, closeFind]);
  // New output doesn't scroll the stream while find is open.
  useEffect(() => {
    stick.hold(findOpen);
  }, [findOpen, stick]);

  if (loadError) {
    return (
      <div className={styles.stateWrap}>
        <Card className={styles.errorCard}>
          <div className={styles.errorTitle}>Couldn't load this conversation</div>
          <div className={styles.errorBody}>
            The session or its transcript failed to load.
          </div>
          <Button variant="primary" onClick={() => setRetryToken((t) => t + 1)}>
            Retry
          </Button>
        </Card>
      </div>
    );
  }

  if (!session) {
    return <div className={styles.stateWrap}>Loading session…</div>;
  }

  // Mid-switch (or after a failed switch), always keep rendering history
  // read-only rather than the terminal — never a blank pane, and never two
  // live transports at once.
  const activeMode: DisplayMode =
    transport.status === "idle"
      ? transport.mode
      : transport.status === "switching"
        ? transport.to
        : transport.attemptedMode;
  // Never shown mid-switch/terminal — terminal mode already has its own live
  // view via SessionTerminalBridge.
  const inChatTurn = activeMode === "chat" && turnPhase === "live";
  // The turn's activity stays open until its reveal drains.
  const turnShowing = activeMode === "chat" && turnPhase !== "idle";
  const lastRow = reveal.rows[reveal.rows.length - 1];
  // Trailing "working" affordance — truthy for the ENTIRE live turn, never
  // just while the tail happens to look busy. It used to go false whenever
  // the last live item was finished text or a completed tool call, which
  // made the indicator flicker out mid-turn between a tool result and the
  // next stream event — exactly when "is it done?" matters most. Tool-pending
  // beats plain "Working…"; WorkingIndicator stays mounted throughout, so its
  // elapsed counter spans the turn. A call parked on a permission prompt is
  // the approval jump's signal, not this one's.
  const working: { tool?: ToolUse } | false = !inChatTurn
    ? false
    : lastRow?.current && lastRow.role === "tool" && lastRow.tool && !lastRow.tool.result
      ? { tool: lastRow.tool }
      : {};
  // Never carry a stale open compose sheet across a switch back to chat and
  // later back to terminal — it would pop back open unprompted.
  if (composeOpen && showHistory) {
    setComposeOpen(false);
  }
  // The hook that would create one of these only ever runs on the chat
  // transport (providers/claudecode/README.md) — a switch to terminal tears
  // the chat process (and any in-flight hook subprocess) down with it, so
  // there is nothing meaningful left to show mid-turn on that transport.
  const livePermissionRequests = activeMode === "chat" ? [...permissionRequests.values()] : [];
  // Still-parked calls, oldest first (a Map keeps first-insertion order; a
  // status upsert doesn't move its entry). Derived, not stored, so the
  // approval jump clears itself the moment the terminal-status event
  // (allowed/denied/expired/cancelled) upserts over a pending entry.
  const pendingPermissions = livePermissionRequests.filter((r) => r.status === "pending");
  const resolvePermission = (id: string, decision: "allow" | "deny") =>
    routerClient.resolvePermissionRequest(sessionId, id, decision);
  // The dock's subagent stream renders the inner rows themselves, so there a
  // subagent call's card sits right under its own row (keyed by tool_use id).
  const permissionsByToolUse = new Map<string, PermissionRequest[]>();
  for (const req of livePermissionRequests) {
    if (!req.tool_use_id) continue;
    permissionsByToolUse.set(req.tool_use_id, [
      ...(permissionsByToolUse.get(req.tool_use_id) ?? []),
      req,
    ]);
  }
  // --- Redesign wiring: dock targets, running-subagent substrip ---
  // The record is authoritative wherever one exists (mergeDelegates).
  const effectiveTasks = mergeDelegates(tasks, delegates);
  // Stop interrupts claude, which ends every subagent and background process
  // with the reply (perTaskStopAffordance is unset — docs/DELEGATES-DESIGN.md
  // C3b). With any of them running, Stop asks first; the reply alone stops at once.
  const requestStop = () => {
    const subagents = [...effectiveTasks.values()].filter((t) => t.running).length;
    const processes = background.length;
    if (subagents + processes === 0) void handleInterrupt();
    else setStopConfirm({ subagents, processes });
  };
  const tasksByToolUseMap = tasksByToolUse(effectiveTasks);
  // The approval jump's "· <subagent>" — named as its task card titles it.
  const subagentName = (req: PermissionRequest) => {
    if (!req.parent_tool_use_id) return undefined;
    const model = tasksByToolUseMap.get(req.parent_tool_use_id);
    return model?.description ?? model?.subagentType ?? "subagent";
  };
  // A running task gets a strip only while its in-stream card is off screen
  // — one copy of each subagent, never the card and its strip together.
  const substripEntries: SubstripEntry[] = [...effectiveTasks.values()]
    .filter((t) => t.running && !visibleTaskCards.has(t.taskId))
    .map((model) => {
      const stream = model.toolUseId ? subagentRows(conv.state, model.toolUseId) : [];
      const last = stream[stream.length - 1];
      const op =
        last?.role === "tool" && last.tool
          ? toolTitle(last.tool, linkName)
          : last?.role === "thinking"
            ? "Thinking…"
            : (model.currentStep ?? model.lastToolName);
      return { model, op };
    });
  // A main-agent process is pinned while its call's row is off screen; a
  // subagent's has no row here (its call is in the subagent's stream) and
  // goes with its subagent — RunningStack nests it under that one's entry.
  const offscreenBackground = background.filter((b) =>
    b.parent_tool_use_id ? true : !b.tool_use_id || !visibleBackgroundRows.has(b.tool_use_id),
  );
  const openTool = (tool: ToolUse) => {
    setShellDockState(sessionId, { open: false }); // never both bottom surfaces at once (hides; the shell keeps running)
    setDockTarget({ type: "tool", toolId: tool.id, tool });
  };
  const openSubagent = (parentToolUseId: string, title: string, subtype?: string) => {
    setShellDockState(sessionId, { open: false });
    setDockTarget({ type: "subagent", parentToolUseId, title, subtype });
  };
  // Resolve the open dock target against the freshest data every render, so
  // a live result/counter lands in the open dock without a re-click.
  let dockView: DockView | null = null;
  if (dockTarget?.type === "tool") {
    let tool = dockTarget.tool;
    let toolTimestamp: string | undefined;
    if (dockTarget.toolId) {
      // Any row: the main list's, or a subagent's.
      for (const r of Object.values(conv.state.rows)) {
        if (r.role === "tool" && r.tool?.id === dockTarget.toolId) {
          tool = r.tool;
          toolTimestamp = r.timestamp;
        }
      }
    }
    dockView = { type: "tool", tool, timestamp: toolTimestamp };
  } else if (dockTarget?.type === "subagent") {
    dockView = {
      type: "subagent",
      parentToolUseId: dockTarget.parentToolUseId,
      title: dockModel?.description ?? dockTarget.title,
      subtype: dockTarget.subtype ?? dockModel?.subagentType,
      items: dockFileItems ?? (dockLive?.length ? dockLive : subagentReplay.get(dockTarget.parentToolUseId)) ?? [],
      task: dockModel,
    };
  }

  // Terminal-view "Ask the agent…": the prompt replaces the compose draft (as
  // a chat prefill replaces the composer's text) and the sheet opens on it.
  const askInCompose = (text: string) => {
    setComposeText(text);
    setComposeOpen(true);
  };
  // A chat send's lifecycle, shared by the composer and Retry on a message
  // that failed — a retry is an ordinary send of the same text. The bubble
  // is the send's own user_message frame; here the working indicator shows
  // at once, ahead of it, and stands down if the send fails before a turn.
  const noteSendStart = () => {
    if (session.activity !== "active") setWaking(true);
    optimisticTurn.current = !turnLive;
    if (!turnLive) convDispatch({ type: "turn", turn: { active: true } });
  };
  const noteSendError = (msg: string) => {
    setWaking(false);
    setSendError(msg);
    setSendFailure(msg);
    if (optimisticTurn.current) convDispatch({ type: "turn", turn: { active: false } });
    optimisticTurn.current = false;
  };
  const noteQueued = (q: QueuedMessage) => {
    setWaking(false);
    optimisticTurn.current = false;
    if (!queuedMessages.some((m) => m.id === q.id)) {
      convDispatch({ type: "queued", queued: [...queuedMessages, q] });
    }
  };
  // A send against a dormant session wakes it (Router: handleSessionMessages)
  // within the POST's own request/response cycle, before this mount's
  // stream (opened before the child existed, dead per the WHATWG
  // EventSource spec — see streamBroken above) reconnects. It does once the
  // session goes active, from its cursor, and the replay brings the turn.
  const noteSent = () => {
    setWaking(false);
    optimisticTurn.current = false;
  };
  // Resends the transcript's text verbatim: any attachment paths the Router
  // appended the first time are already in it, so the agent gets the same
  // message, and the identical text is what folds the failed try away.
  const retrySend = async (text: string) => {
    setSendError(undefined);
    noteSendStart();
    try {
      const result = await routerClient.sendMessage(sessionId, { text, mentions: [], queue: true });
      if (result.status === "queued") noteQueued(result.queued);
      else noteSent();
    } catch (err) {
      noteSendError(err instanceof Error ? err.message : String(err));
    }
  };
  // The session dock's props, shared by both views (chat mounts the composer
  // inside it; terminal adds the compose segment and sheet).
  const dockProps = {
    session,
    usage: lastUsage,
    contextWindow: lastUsage ? contextWindowForModel(session.model) : undefined,
    onCompact: compaction.compacts ? () => void handleCompactNow() : undefined,
    onModeChange: (m: DisplayMode) => void handleToggle(m),
    agents,
    workspaceName: sessionWorkspace?.name ?? undefined,
    checkoutBranch,
    onChangeModel: handleChangeModel,
    onChangeTrust: handleChangeTrust,
    work: {
      onCommit: handleCommit,
      onLand: handleLand,
      // Chat drafts into the composer. Terminal has no composer mounted, so
      // the draft opens the compose sheet instead: the user reviews it there
      // and pastes it into the TUI themselves (never sent on its own).
      onAskAgent: !showHistory && terminalBridge ? askInCompose : worktreeActions.onAskAgent,
      landing: worktree.landing,
      committing: worktree.committing,
      loadStatus: loadWorktreeStatus,
    },
    landOutcome: latestLandOutcome(worktree.cards),
  };
  // Terminal compose: a draft survives closing the sheet (an accidental Esc
  // never loses a long prompt); only a paste clears it.
  const openCompose = () => setComposeOpen(true);
  const closeCompose = () => {
    setComposeOpen(false);
    terminalViewRef.current?.focus();
  };
  const toggleCompose = () => (composeOpen ? closeCompose() : openCompose());
  const composeSheet =
    composeOpen && terminalBridge ? (
      <div
        // One owner for Esc (and the compose chord, which closes too) whether
        // focus is in Monaco or on a button: capture-phase, before Monaco's
        // own keybinding service sees the keydown.
        onKeyDownCapture={(e) => {
          if (e.key === "Escape" || matches("expandEditor", e)) {
            e.preventDefault();
            e.stopPropagation();
            closeCompose();
          }
        }}
      >
        <ExpandedComposer
          value={composeText}
          onChange={setComposeText}
          onSend={(text) => {
            terminalBridge.write(sessionId, formatBracketedPaste(text));
            setComposeText("");
            closeCompose();
          }}
          onCollapse={closeCompose}
          sendLabel="Paste into terminal"
          showBusyControls={false}
          hintSuffix="sent as a bracketed paste"
        />
      </div>
    ) : undefined;

  return (
    <LinkNamesProvider resolve={linkName}>
      <div className={styles.tab} ref={tabRef}>
        <CommitDialog
          open={commitDialog !== null}
          sessionId={sessionId}
          routerClient={routerClient}
          fastModel={commitSettings.fastModel}
          initialError={commitDialog?.initialError}
          onClose={() => setCommitDialog(null)}
          onCommitted={worktree.noteCommit}
          onOpenFile={onOpenFile}
        />

        <div className={styles.body}>
          {showHistory ? (
            <>
              <div
                className={styles.stream}
                ref={stick.ref}
                onScroll={() => {
                  stick.onScroll();
                  olderPages.onScroll();
                }}
                data-testid="conversation-stream"
              >
                {provisioning && (
                  <div className={styles.provisioningWrap}>
                    <ProvisioningStrip
                    sessionId={sessionId}
                    routerClient={routerClient}
                    onDone={onProvisioningDone}
                  />
                  </div>
                )}
                {conv.rows.length === 0 && worktree.cards.length === 0 && !inChatTurn ? (
                  <>
                    <div className={styles.emptyTranscript}>
                      {leaderId
                        ? `Waiting for ${linkName(leaderId) ?? "its leader"} to send the first message`
                        : "No conversation yet"}
                    </div>
                    {/* No rows to anchor to — every card is a fallback one. */}
                    {livePermissionRequests.map((req) => (
                      <PermissionCard key={req.id} request={req} onResolve={resolvePermission} />
                    ))}
                  </>
                ) : (
                  <ChatRenderer
                    rows={reveal.rows}
                    links={links}
                    viewedSessionId={sessionId}
                    linkName={linkName}
                    attachmentUrl={(id, variant) =>
                      routerClient.attachmentUrl({ session: sessionId }, id, variant)
                    }
                    turnActive={turnShowing}
                    foldNewCode={wordsPerMinute !== null}
                    toolStatuses={toolStatuses}
                    background={backgroundByTool}
                    tasks={tasksByToolUseMap}
                    selectedToolId={dockTarget?.type === "tool" ? dockTarget.toolId : undefined}
                    onOpenTool={openTool}
                    onOpenTask={(toolUseId, tool) =>
                      openSubagent(
                        toolUseId,
                        tasksByToolUseMap.get(toolUseId)?.description ?? toolTitle(tool, linkName),
                        tasksByToolUseMap.get(toolUseId)?.subagentType,
                      )
                    }
                    worktreeCards={worktree.cards}
                    worktreeActions={worktreeActions}
                    permissionRequests={livePermissionRequests}
                    onResolvePermission={resolvePermission}
                    permissionReveal={permissionReveal}
                    onRetry={(text) => void retrySend(text)}
                    sendFailure={sendFailure}
                    find={find.request}
                    onFindStatus={find.onStatus}
                  />
                )}
                {transport.status !== "idle" && (
                  <div className={styles.transportBanner}>
                    {transport.status === "switching" ? (
                      <span>Switching to {transport.to === "terminal" ? "terminal" : "chat"}…</span>
                    ) : (
                      <>
                        <span>
                          {transport.code === "busy"
                            ? "Can't switch — a turn is in flight."
                            : "Couldn't switch transport."}
                        </span>
                        <div className={styles.transportActions}>
                          <Button
                            variant="secondary"
                            size="sm"
                            onClick={() => void handleToggle(transport.attemptedMode)}
                          >
                            Retry
                          </Button>
                          {transport.code === "busy" && (
                            <Button
                              variant="secondary"
                              size="sm"
                              onClick={() => void handleInterruptAndSwitch(transport.attemptedMode)}
                            >
                              Interrupt and switch
                            </Button>
                          )}
                          {transport.code !== "busy" && transport.attemptedMode === "terminal" && (
                            <Button
                              variant="secondary"
                              size="sm"
                              onClick={() => void handleToggle("chat")}
                            >
                              Return to chat
                            </Button>
                          )}
                        </div>
                      </>
                    )}
                  </div>
                )}
                {waking && (
                  <div className={styles.transportBanner}>
                    <span>Waking the agent…</span>
                  </div>
                )}
                <CompactionStatus
                  status={compactStatus}
                  onDismiss={() => {
                    writeCompactDismissedAt(sessionId);
                    setCompactStatus(undefined);
                  }}
                />
                {/* Pinned strip (T16): the approval jump, running-subagent
                    substrip, turn-long working indicator and queued messages
                    float over the bottom of the scrollable stream, just above
                    the session dock — none can scroll off screen while the
                    conversation is on screen. The dock itself sits below the
                    stream, outside it, so the scrollbar ends at its top edge. */}
                <div className={styles.pinned} data-testid="pinned-cluster" ref={pinnedRef}>
                  <ApprovalJump
                    pending={pendingPermissions}
                    subagentName={subagentName}
                    scopeRef={tabRef}
                    streamRef={streamRef}
                    pinnedRef={pinnedRef}
                    reveal={permissionReveal}
                    onReveal={revealPermission}
                  />
                  <RunningStack
                    key={sessionId}
                    agents={substripEntries}
                    processes={offscreenBackground}
                    onOpen={(model) =>
                      model.toolUseId &&
                      openSubagent(model.toolUseId, model.description ?? "Subagent", model.subagentType)
                    }
                  />
                  {working && (
                    <WorkingIndicator
                      tool={working.tool}
                      activity={currentActivity}
                      since={agentSince ?? conv.state.turn.startedAt}
                    />
                  )}
                  <QueuedMessages
                    queued={queuedMessages}
                    onCancel={handleCancelQueued}
                    className={styles.queuedAbove}
                  />
                  {showAll.present && (
                    // The paced reveal trails the agent by more than a couple of
                    // seconds' reading — offer to skip to the end of it.
                    <div className={styles.revealAll} data-leaving={showAll.leaving || undefined}>
                      <Chip tone="info" onClick={() => reveal.revealAll()}>
                        Show all
                      </Chip>
                    </div>
                  )}
                </div>
              </div>
              {find.open && <FindBar {...find.bar} streamRef={streamRef} />}
              <SessionDock {...dockProps} mode="chat">
                <Composer
                  routerClient={routerClient}
                  sessionId={sessionId}
                  session={session}
                  links={links}
                  agentId={session.agent}
                  provider={session.provider}
                  usage={lastUsage}
                  contextWindow={dockProps.contextWindow}
                  mode={activeMode}
                  onModeChange={dockProps.onModeChange}
                  onCompact={dockProps.onCompact}
                  disabled={transport.status !== "idle" || waking}
                  busy={inChatTurn}
                  onStop={requestStop}
                  initialValue={pendingFirstMessage}
                  prefill={composerPrefill}
                  autoSendInitial={Boolean(pendingFirstMessage)}
                  onSendStart={noteSendStart}
                  onSendError={noteSendError}
                  onAutoSendStart={onPendingFirstMessageHandled}
                  onUpdateSession={handleUpdateSession}
                  onCommandExecuted={(message, action) => setCommandToast({ tone: "ok", message, action })}
                  onCommandError={(message) => setCommandToast({ tone: "bad", message })}
                  onOpenTask={onOpenTask}
                  onCompactSession={compaction.compacts ? (i) => void handleCompactNow(i) : undefined}
                  onQueued={noteQueued}
                  onSent={noteSent}
                />
              </SessionDock>
            </>
          ) : (
            <>
              <div className={styles.stream} ref={streamRef} data-testid="conversation-stream">
                {terminalBridge ? (
                  <TerminalView
                    ref={terminalViewRef}
                    bridge={terminalBridge}
                    className={styles.terminal}
                    onComposeOpen={openCompose}
                    onToggleView={() => void handleToggle("chat")}
                  />
                ) : (
                  <div className={styles.ptyUnavailable}>Connecting…</div>
                )}
              </div>
              {/* The xterm is the input: no composer, chips, queue or working
                  line here. The dock's height is fixed in this view, so the
                  xterm fits once; the compose sheet floats over it rather than
                  resizing the PTY. */}
              <SessionDock
                {...dockProps}
                mode="terminal"
                onComposeToggle={terminalBridge ? toggleCompose : undefined}
                composeOpen={composeOpen}
                sheet={composeSheet}
              />
            </>
          )}

          {shellOpen &&
            (shellBridge ? (
              <ShellDock
                bridge={shellBridge}
                cwd={session.cwd ?? session.worktree_dir}
                height={shellHeight}
                onHeightChange={(height) => setShellDockState(sessionId, { height })}
                onHide={() => setShellDockState(sessionId, { open: false })}
                onEnd={() => {
                  // The busy-confirmation dialog (docs/SHELL-DOCK-DESIGN.md D4) is a later step.
                  shellBridge.end();
                  setShellDockState(sessionId, { open: false });
                }}
              />
            ) : (
              <div className={styles.ptyUnavailable}>
                Shell requires the Electron shell.
              </div>
            ))}
        </div>

        <ConfirmDialog
          // A turn that ends while the question is up leaves nothing to stop.
          open={stopConfirm !== undefined && inChatTurn}
          title="Stop everything?"
          description={stopConfirm && `Stopping the reply also ends ${stopSummary(stopConfirm)}.`}
          confirmLabel="Stop everything"
          cancelLabel="Keep working"
          onConfirm={() => {
            setStopConfirm(undefined);
            void handleInterrupt();
          }}
          onCancel={() => setStopConfirm(undefined)}
        />

        {dockView && (
          <ActivityDock
            view={dockView}
            sessionId={sessionId}
            client={routerClient}
            statuses={toolStatuses}
            permissions={permissionsByToolUse}
            onResolvePermission={resolvePermission}
            onClose={() => setDockTarget(null)}
            onOpenPage={
              onOpenDelegate && dockView.type === "subagent" && dockView.task
                ? () => {
                    setDockTarget(null);
                    onOpenDelegate(dockView.task!.taskId);
                  }
                : undefined
            }
          />
        )}

        {sendError && (
          <Toast
            tone="bad"
            title="Couldn't send your message"
            description={sendError}
            onDismiss={() => setSendError(undefined)}
          />
        )}

        {commandToast && (
          <Toast
            tone={commandToast.tone}
            title={commandToast.tone === "ok" ? commandToast.message : "Command failed"}
            description={commandToast.tone === "bad" ? commandToast.message : undefined}
            action={
              commandToast.action && {
                label: commandToast.action.label,
                onClick: () => {
                  setCommandToast(undefined);
                  commandToast.action?.onClick();
                },
              }
            }
            onDismiss={() => setCommandToast(undefined)}
          />
        )}
      </div>
    </LinkNamesProvider>
  );
}

/** "2 subagents and 1 background process" — what a confirmed Stop ends. */
function stopSummary({ subagents, processes }: { subagents: number; processes: number }): string {
  const parts = [
    subagents > 0 && `${subagents} ${subagents === 1 ? "subagent" : "subagents"}`,
    processes > 0 && `${processes} ${processes === 1 ? "background process" : "background processes"}`,
  ].filter(Boolean);
  return parts.join(" and ");
}
