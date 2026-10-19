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
  TranscriptMessage,
  Usage,
} from "../router/types";
import { foldToolResults } from "../router/toolPairing";
import { useSessionLinks } from "../features/sessions/state/useSessionLinks";
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
  appendSubagentItem,
  applyTaskEvent,
  CONVERSATION_EVENT_TYPES,
  SUBAGENT_STREAM_EVENT_TYPES,
  finishRunningTasks,
  foldHistoryEvents,
  transcriptSubStream,
  mergeDelegates,
  replayedCompactStatus,
  resumeRunningTasks,
  tasksByToolUse,
  type ConversationHistory,
  type SubStreamItem,
  type TaskModel,
} from "./activity/conversationItems";
import { LinkNamesProvider, useLinkNames } from "./activity/linkNames";
import { formatTokens, toolTitle } from "./activity/toolDisplay";
import { Chip } from "../components/Chip/Chip";
import { FADE_OUT_MS, usePresence } from "../components/Motion";
import { useStreamPacer } from "./streaming/useStreamPacer";
import { revealedToMessages } from "./streaming/streamPacer";
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

// Driven purely by the stream, not by "who sent the message" — the same
// transition applies whether this client's own send started the turn or a
// linked-session inject/mid-turn mount is the first thing observed, so both
// fall out of the same code path with no separate case to maintain.
// The in-flight turn is built directly from the stream (text tokens, tool_use,
// tool_result) and is the SOLE source of truth for it, reconciled with the
// persisted transcript exactly once, on turn_end — no mid-turn transcript poll
// to make streamed text rubber-band. The ordered items live in a stream pacer
// (useStreamPacer) so the reveal rate can be decoupled from token arrival;
// ConversationTab keeps only the turn's phase + a `blocked` reason here.
// "revealing" is a turn the agent has finished whose paced reveal is still
// playing: the composer and working indicator are already idle (the agent
// is), while the live items stay on screen until the reveal drains.
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
  const [messages, setMessages] = useState<TranscriptMessage[]>([]);
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
  const [turnPhase, setTurnPhase] = useState<TurnPhase>("idle");
  // session.workspace is a reference (id or name), not the name the dock's
  // ⎇ segment prints — resolved here, off the session this tab already holds,
  // and kept live by the hook's workspace.* subscription.
  const sessionWorkspace = useWorkspaceForRef(routerClient, session?.workspace);
  const agents = useAgentIndex(routerClient);
  const checkoutBranch = useCheckoutBranch(routerClient, session);
  // Paces the live turn's reveal (a reading pace in words per minute; Native
  // by default). Owns the ordered items; its callbacks are stable across
  // renders.
  const wordsPerMinute = streamPaceToWpm(useStreamSpeed(routerClient));
  const pacer = useStreamPacer(wordsPerMinute);
  const showAll = usePresence(pacer.behind, FADE_OUT_MS);
  const {
    enqueueText: pacerEnqueueText,
    enqueueTool: pacerEnqueueTool,
    enqueueThinking: pacerEnqueueThinking,
    applyToolResult: pacerApplyToolResult,
    finish: pacerFinish,
    flush: pacerFlush,
    reset: pacerReset,
  } = pacer;
  // Bumped by each turn start (beginTurn): an end-of-turn reconcile that
  // resolves after the next turn started must leave that turn alone.
  const turnGenRef = useRef(0);
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
  // exactly that dead-on-arrival case; streamGeneration is bumped once the
  // session is confirmed active again (session.activity_changed, picked up
  // by the WORKSPACE_SESSION_EVENT_TYPES subscription below) to force one
  // fresh connection — not a timer/backoff loop, just a single deliberate
  // re-open keyed to a real state transition.
  const [streamBroken, setStreamBroken] = useState(false);
  const [streamGeneration, setStreamGeneration] = useState(0);
  const links = useSessionLinks([sessionId]);
  const linkName = useLinkNames(routerClient, sessionId, links);
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
  // Each subagent's own conversation stream, keyed by the spawning Agent
  // tool_use id. Parent-tagged /output events land here and NEVER in the
  // main stream (the interleave bug this redesign fixes); history replays
  // from session.subagent_event — claude-code's transcript omits sidechains.
  const [subagentStreams, setSubagentStreams] = useState<Map<string, SubStreamItem[]>>(new Map());
  // Whole historical thinking blocks (session.thinking), merged into the
  // rendered stream by timestamp. Live thinking rides the pacer instead.
  const [thinkingHistory, setThinkingHistory] = useState<{ text: string; timestamp?: string }[]>([]);
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

  const appendSubagent = useCallback(
    (
      parentId: string,
      ev: { kind: "text" | "tool_use" | "tool_result" | "thinking"; text?: string; tool?: ToolUse; is_error?: boolean },
    ) => {
      // Live wire frames carry no timestamps — stamp arrival time so
      // subagent rows and their group headers can report durations.
      const stamped = { ...ev, timestamp: new Date().toISOString() };
      setSubagentStreams((m) => appendSubagentItem(m, parentId, stamped));
    },
    [],
  );

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

  // The user's messages waiting behind the in-flight turn. The session
  // process owns the queue (POST /messages with queue:true), so it survives
  // this pane unmounting; this is only its mirror — fetched on mount, then
  // kept current by session.queue_changed.
  const [queuedMessages, setQueuedMessages] = useState<QueuedMessage[]>([]);

  useEffect(() => {
    let cancelled = false;
    setLoadError(false);
    setSession(null);
    setMessages([]);
    setTurnPhase("idle");
    pacerReset();
    setPermissionRequests(new Map());
    setLastUsage(undefined);
    setCompactStatus(undefined);
    setAgentSince(undefined);
    setPendingReplay(null);
    setQueuedMessages([]);
    setToolStatuses(new Map());
    setTasks(new Map());
    setDelegates(new Map());
    setSubagentStreams(new Map());
    setThinkingHistory([]);
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
          // Reopened mid-turn: the /output stream never replays the frames
          // that would have flipped the turn live, so the record's mirrored
          // agent state does it — otherwise the working indicator stays away
          // until the turn's next frame. Only a live Runner clears the
          // mirror, so an inactive session's "working" is a stale leftover.
          if (s.activity === "active" && s.agent_state?.state === "working") {
            setTurnPhase("live");
            setAgentSince(s.agent_state.since);
            if (s.agent_state.label) setCurrentActivity((a) => a ?? s.agent_state?.label);
          }
        }
      })
      .catch(() => {
        if (!cancelled) setLoadError(true);
      });
    void routerClient
      .getTranscript(sessionId)
      .then((m) => {
        if (!cancelled) setMessages(m);
      })
      .catch(() => {
        if (!cancelled) setLoadError(true);
      });
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
      .listQueuedMessages(sessionId)
      .then((q) => {
        // Merge rather than replace: a queue_changed that landed while this
        // was in flight is fresher, and it carries the whole queue anyway.
        if (!cancelled) setQueuedMessages((live) => (live.length > 0 ? live : q));
      })
      .catch(() => {});
    void routerClient
      .getPermissionRequests(sessionId)
      .then((reqs) => {
        if (cancelled) return;
        setPermissionRequests(new Map(reqs.map((r) => [r.id, r])));
      })
      .catch(() => {});
    // History parity (T17): the control-plane event log is the only
    // historical source for subagent streams, task lifecycles, main-agent
    // thinking, and out-of-band status changes — the provider transcript
    // omits all four. Merge live-wins: anything the stream delivered while
    // this fetch was in flight is fresher than the replay.
    void getAllEvents(routerClient, { sessionId, types: CONVERSATION_EVENT_TYPES })
      .then((envelopes) => {
        if (cancelled) return;
        const h = foldHistoryEvents(envelopes);
        setToolStatuses((live) => new Map([...h.statuses, ...live]));
        setTasks((live) => new Map([...h.tasks, ...live]));
        setSubagentStreams((live) => new Map([...h.subagentStreams, ...live]));
        setThinkingHistory(h.thinking);
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
  }, [routerClient, sessionId, retryToken, pacerReset]);

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
  // WORKSPACE_SESSION_EVENT_TYPES) rather than opening its own — GET
  // /transcript has no push notification of its own (see refetchAfterTurn
  // below), and a linked-session inject/digest lands on this session's
  // transcript out of band from anything this viewer sent, so it needs a
  // server-scoped trigger to refetch rather than waiting on the stream's
  // own turn_end (which only fires for turns this session's provider runs).
  useEffect(() => {
    return subscribeToEvents(
      { sessionId, types: WORKSPACE_SESSION_EVENT_TYPES },
      (envelope) => {
        if (envelope.type === "link.message_injected") {
          void routerClient.getTranscript(sessionId).then(setMessages);
        } else if ((SESSION_SHELL_REFETCH_EVENT_TYPES as string[]).includes(envelope.type)) {
          refetchSession();
        }
        // Anything else in WORKSPACE_SESSION_EVENT_TYPES (task.*/stage.*/
        // file.changed/note.created/pipeline.*) is there only for other
        // subscribers sharing this bus (usePipelineRun) — this tab has
        // nothing to do with them.
      },
    );
  }, [refetchSession, routerClient, sessionId]);

  // The session's live data-plane stream (services/session's GET /output,
  // proxied via the Router) — one subscription per session, independent of
  // display mode. This is the channel SessionTerminalBridge rides for
  // terminal-mode output; it also stays open across a chat<->terminal
  // switch, since the switch is server-side (SwitchTransport), not a
  // reconnect of this stream.
  useEffect(() => {
    const source = routerClient.streamSession(sessionId);
    setEventSource(source);
    setBackground([]);
    setStreamBroken(false);
    return () => {
      source.close();
      setEventSource(null);
    };
  }, [routerClient, sessionId, streamGeneration]);

  // Reconnects the stream exactly once a dead-on-arrival connection
  // (streamBroken) coincides with the session having gone active — see the
  // streamBroken/streamGeneration declaration above.
  useEffect(() => {
    if (streamBroken && session?.activity === "active") {
      setStreamBroken(false);
      setStreamGeneration((g) => g + 1);
    }
  }, [streamBroken, session?.activity]);

  // Forced reattach for "Restart agent" (SessionWorkspace's kebab) — fires
  // unconditionally on a successful bounce, regardless of the cached
  // session.activity value, unlike the passive reconnect above. See
  // streamReattachSignal.ts for why this can't just wait on that path.
  useEffect(() => {
    const handler = (e: Event) => {
      const detail = (e as CustomEvent<StreamReattachDetail>).detail;
      if (detail?.sessionId !== sessionId) return;
      setStreamBroken(false);
      setStreamGeneration((g) => g + 1);
    };
    window.addEventListener(STREAM_REATTACH_EVENT, handler);
    return () => window.removeEventListener(STREAM_REATTACH_EVENT, handler);
  }, [sessionId]);

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

  const messagesRef = useRef<TranscriptMessage[]>(messages);
  messagesRef.current = messages;

  // Latest turn phase, read from callbacks that fire outside the render
  // that set it (EventSource "open", visibilitychange) — same ref-mirrors-
  // state idiom as messagesRef above.
  const turnPhaseRef = useRef(turnPhase);
  turnPhaseRef.current = turnPhase;

  // GET /transcript has no push notification for turn completion (no domain
  // event fires per-turn, only on session start/shutdown), so the persisted
  // reply only becomes visible by re-fetching once the stream's turn_end
  // frame says the turn is over. One fetch, one retry 300ms later if the
  // transcript hasn't caught up yet (or the fetch throws) — never an
  // unbounded poll; the frozen streamed bubble is the fallback if both fail.
  // A transcript that hasn't grown never replaces the pane: it can be behind
  // it — the harness writes a just-sent user message to its file a beat
  // after receiving it, and the bubble added on send would vanish until the
  // next turn's end.
  const refetchAfterTurn = useCallback(async (stillCurrent: () => boolean = () => true) => {
    const baseline = messagesRef.current.length;
    for (let attempt = 0; attempt < 2; attempt++) {
      try {
        const fresh = await routerClient.getTranscript(sessionId);
        if (!stillCurrent()) return;
        if (fresh.length > baseline) {
          setMessages(fresh);
          return;
        }
      } catch {
        // retry below
      }
      if (attempt === 0) await new Promise((resolve) => setTimeout(resolve, 300));
    }
  }, [routerClient, sessionId]);

  // Ends the live turn. The agent is done, so the composer and indicator go
  // idle now; a paced reveal keeps playing ("revealing") and, once drained,
  // reconciles once with the persisted transcript and drops the live items —
  // frozen-visible throughout, so there's never a flash to empty. A send in
  // the meantime takes the reveal over (handleSendStart), and the reconcile
  // then stands down.
  const endTurn = useCallback(() => {
    // The ref too, not just state: a second end-of-turn signal (the stream's
    // turn_end and the agent going idle both arrive) mustn't see "live".
    if (turnPhaseRef.current === "live") turnPhaseRef.current = "revealing";
    setTurnPhase((p) => (p === "live" ? "revealing" : p));
    // A Stop question outlives nothing: the turn it asked about is over.
    setStopConfirm(undefined);
    pacerFinish(() => {
      const gen = turnGenRef.current;
      const stillCurrent = () => turnGenRef.current === gen;
      void refetchAfterTurn(stillCurrent).finally(() => {
        if (!stillCurrent()) return;
        setTurnPhase((p) => (p === "revealing" ? "idle" : p));
        pacerReset();
      });
    });
  }, [pacerFinish, refetchAfterTurn, pacerReset]);

  // A turn starts — the user's send, a queued message going out, or one
  // this pane only hears about from the agent state. If the last reply is
  // still revealing, show the rest of it at once and hand it to the history
  // as plain transcript rows, so the new user message lands after it. The
  // next turn's own end reconciles the whole thing with the persisted
  // transcript.
  const beginTurn = useCallback(() => {
    turnGenRef.current++;
    if (turnPhaseRef.current === "revealing") {
      const revealed = pacerFlush();
      setMessages((m) => [...m, ...revealedToMessages(revealed)]);
    }
    turnPhaseRef.current = "live"; // ahead of the render, for the next signal
    setTurnPhase("live");
  }, [pacerFlush]);

  // The chat-mode "Stop" button — the ⌘/Ctrl-C of the terminal. Interrupts the
  // in-flight turn, then finalizes locally exactly as onTurnEnd does rather
  // than waiting on the child's turn_end frame (the /output stream is
  // known-unreliable for the current turn — see refetchAfterTurn), so the stop
  // reads as immediate. The child still sends its own turn_end; that just
  // reconciles onto an already-idle turn.
  const handleInterrupt = useCallback(async () => {
    try {
      await routerClient.interrupt(sessionId);
    } catch (err) {
      setSendError(err instanceof Error ? err.message : "Failed to stop the agent.");
      return;
    }
    setCurrentActivity(undefined);
    setTasks((m) => finishRunningTasks(m));
    endTurn();
  }, [routerClient, sessionId, endTurn]);

  // Durable turn-completion reconciliation: /output is ephemeral and can
  // shed frames under an end-of-turn burst (the hub closes overflowing
  // subscriptions rather than silently gapping, but a frame can still be
  // lost to a dying connection). The runner's session.turn_ended envelope
  // rides the control-plane event log — lossless — so it finalizes any turn
  // the stream's own turn_end frame failed to close, instead of leaving the
  // pane frozen on its last streamed item until a remount. Idempotent when
  // both signals arrive: an idle pane just re-checks the transcript.
  //
  // session.agent_state rides the same log: "working" marks a turn in flight
  // even when this tab saw none of its /output frames (another client's send,
  // a linked-session wake, a reconnect) and carries the turn's start for the
  // elapsed counter; "idle" closes the turn exactly as turn_ended does — and
  // unlike turn_ended, it fires for a turn with no digest delta too.
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
          // own next frame is (onText/onTool/onThinking), being ordered.
          if (turnPhaseRef.current === "idle") beginTurn();
          setAgentSince(state.since || envelope.timestamp);
          // The stream's own tool/thinking frames keep the ticker current;
          // the server label only fills it when none has arrived yet.
          if (state.label) setCurrentActivity((a) => a ?? state.label);
          return;
        }
        if (state?.state !== "idle") return;
      }
      if (turnPhaseRef.current === "live") {
        setCurrentActivity(undefined);
        setTasks((m) => finishRunningTasks(m));
        endTurn();
      } else if (turnPhaseRef.current === "idle") {
        // A late signal for a turn this pane already closed. A turn starting
        // meanwhile (a queued message goes out exactly now) owns the pane.
        const gen = turnGenRef.current;
        void refetchAfterTurn(() => turnGenRef.current === gen);
      }
      // "revealing": the turn already ended here; its reveal reconciles.
    });
  }, [sessionId, beginTurn, endTurn, refetchAfterTurn]);

  // A queued message going out is the start of a turn this pane didn't
  // send, so it gets the user bubble a direct send gets from onSent. The
  // turn-end transcript refetch reconciles either way.
  useEffect(() => {
    return subscribeToEvents({ sessionId, types: ["session.queue_changed"] }, (envelope) => {
      const change = envelope.payload as Partial<QueueChanged> | undefined;
      if (!change) return;
      setQueuedMessages(change.queued ?? []);
      if (change.delivered) {
        const text = change.delivered.text;
        if (turnPhaseRef.current !== "live") beginTurn();
        setMessages((m) => [...m, { role: "user", text, timestamp: new Date().toISOString() }]);
      }
      if (change.failed) {
        setSendError(`Couldn't send your queued message: ${change.error ?? "unknown error"}`);
      }
    });
  }, [sessionId, beginTurn]);

  // Queued messages live in the session process, so a session that stops
  // has none, whatever the last event said.
  useEffect(() => {
    if (session && session.activity !== "active") setQueuedMessages([]);
  }, [session]);

  const handleCancelQueued = useCallback(
    (queuedId: string) => {
      setQueuedMessages((q) => q.filter((m) => m.id !== queuedId));
      routerClient.cancelQueuedMessage(sessionId, queuedId).catch((err: unknown) => {
        setSendError(err instanceof Error ? err.message : "Couldn't remove the queued message.");
      });
    },
    [routerClient, sessionId],
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

  // Consumes the same live output stream SessionTerminalBridge rides for
  // terminal mode (set up above) — chat mode folds its text/tool_use/
  // turn_end/error frames into the turn-state machine instead of opening a
  // second stream.
  useEffect(() => {
    if (!eventSource) return;
    // Parent-tagged events are a subagent's sidechain: they route to that
    // subagent's own stream (dock + substrip), NEVER into the main
    // transcript — interleaving them was the live bug this redesign fixes.
    const onText = (ev: MessageEvent<string>) => {
      const parsed = JSON.parse(ev.data) as StreamEvent;
      if (parsed.kind !== "text") return;
      if (parsed.parent_tool_use_id) {
        appendSubagent(parsed.parent_tool_use_id, { kind: "text", text: parsed.text });
        return;
      }
      // Only the main agent's own output is a turn: a backgrounded
      // subagent keeps streaming after its parent's turn ended, and nothing
      // would ever close a phase it opened.
      if (turnPhaseRef.current !== "live") beginTurn();
      pacerEnqueueText(parsed.text);
    };
    const onTool = (ev: MessageEvent<string>) => {
      const parsed = JSON.parse(ev.data) as StreamEvent;
      if (parsed.kind !== "tool_use") return;
      if (parsed.parent_tool_use_id) {
        appendSubagent(parsed.parent_tool_use_id, { kind: "tool_use", tool: parsed.tool });
        return;
      }
      if (turnPhaseRef.current !== "live") beginTurn();
      pacerEnqueueTool(parsed.tool, new Date().toISOString());
      setCurrentActivity(toolTitle(parsed.tool, linkNameRef.current));
    };
    const onToolResult = (ev: MessageEvent<string>) => {
      const parsed = JSON.parse(ev.data) as StreamEvent;
      if (parsed.kind !== "tool_result") return;
      if (parsed.parent_tool_use_id) {
        appendSubagent(parsed.parent_tool_use_id, {
          kind: "tool_result",
          tool: parsed.tool,
          text: parsed.text,
          is_error: parsed.is_error,
        });
        return;
      }
      // Attach the result to its live tool card (matched by id) so the card
      // shows its outcome as it arrives — never a silent no-op. An is_error
      // result renders as the card's own error state; it is NOT a permission
      // signal (claude reports every ordinary tool failure this way — a
      // non-zero exit, a missing file), so it must not raise an approval
      // signal. Genuine permission stalls arrive as permission_request
      // events and drive the approval jump below.
      pacerApplyToolResult(parsed.tool?.id, {
        text: parsed.text,
        is_error: parsed.is_error,
        timestamp: new Date().toISOString(),
      });
    };
    const onThinking = (ev: MessageEvent<string>) => {
      const parsed = JSON.parse(ev.data) as StreamEvent;
      if (parsed.kind !== "thinking") return;
      if (parsed.parent_tool_use_id) {
        if (parsed.text) {
          appendSubagent(parsed.parent_tool_use_id, { kind: "thinking", text: parsed.text });
        }
        return;
      }
      // A whole block is the main agent's. A bare token tick may not be:
      // claude-code streams a subagent's thinking_tokens untagged, so a tick
      // only feeds a turn already live (session.agent_state opens turns).
      if (parsed.text && turnPhaseRef.current !== "live") beginTurn();
      // A whole block joins the live stream as a think row; a bare
      // thinking_tokens count is just the indicator's ticker.
      if (parsed.text) pacerEnqueueThinking(parsed.text);
      setCurrentActivity(
        parsed.thinking_tokens
          ? `Thinking · ${formatTokens(parsed.thinking_tokens)} tokens`
          : "Thinking…",
      );
    };
    const onTask = (ev: MessageEvent<string>) => {
      const parsed = JSON.parse(ev.data) as StreamEvent;
      if (parsed.kind !== "task") return;
      // Not a turn signal: lifecycle lines arrive between turns too.
      setTasks((m) => applyTaskEvent(m, parsed.task, true));
    };
    const onBackground = (ev: MessageEvent<string>) => {
      const parsed = JSON.parse(ev.data) as StreamEvent;
      if (parsed.kind !== "background") return;
      setBackground(parsed.background ?? []);
    };
    const onPermissionRequest = (ev: MessageEvent<string>) => {
      const parsed = JSON.parse(ev.data) as StreamEvent;
      if (parsed.kind !== "permission_request") return;
      upsertPermissionRequest(parsed.permission);
    };
    const onCompact = (ev: MessageEvent<string>) => {
      const parsed = JSON.parse(ev.data) as StreamEvent;
      if (parsed.kind !== "compact") return;
      setCompactStatus(parsed.compact);
    };
    const onTurnEnd = (ev: MessageEvent<string>) => {
      // turn_end carries this turn's usage — the only place it's reported, so
      // it feeds the dock's context rim. One that counts no tokens (a /compact
      // turn the CLI gave no figure for) leaves the rim where it was.
      if (typeof ev.data === "string") {
        try {
          const parsed = JSON.parse(ev.data) as StreamEvent;
          if (parsed.kind === "turn_end" && parsed.usage && countsTokens(parsed.usage)) {
            setLastUsage(parsed.usage);
          }
        } catch {
          // non-JSON turn_end frame — leave the meter on its last value
        }
      }
      setCurrentActivity(undefined);
      // A turn ending with no task_notification means those subagents never
      // finished — interrupted-shape, never a spinner that ticks forever.
      setTasks((m) => finishRunningTasks(m));
      endTurn();
    };
    const onError = (ev: MessageEvent<string>) => {
      // A native transport-level failure (e.g. the initial GET /output 404
      // against a dormant session) fires a plain Event, not a MessageEvent —
      // no `.data` to parse, and not a StreamEvent from the provider at all.
      if (typeof ev.data !== "string") {
        setStreamBroken(true);
        return;
      }
      const parsed = JSON.parse(ev.data) as StreamEvent;
      setTurnPhase("idle");
      pacerReset();
      setCurrentActivity(undefined);
      setTasks((m) => finishRunningTasks(m));
      setSendError(parsed.kind === "error" ? parsed.text : "The agent hit an error.");
    };
    eventSource.addEventListener("text", onText);
    eventSource.addEventListener("tool_use", onTool);
    eventSource.addEventListener("tool_result", onToolResult);
    eventSource.addEventListener("thinking", onThinking);
    eventSource.addEventListener("task", onTask);
    eventSource.addEventListener("background", onBackground);
    eventSource.addEventListener("permission_request", onPermissionRequest);
    eventSource.addEventListener("turn_end", onTurnEnd);
    eventSource.addEventListener("compact", onCompact);
    eventSource.addEventListener("error", onError);
    return () => {
      eventSource.removeEventListener("text", onText);
      eventSource.removeEventListener("tool_use", onTool);
      eventSource.removeEventListener("tool_result", onToolResult);
      eventSource.removeEventListener("thinking", onThinking);
      eventSource.removeEventListener("task", onTask);
      eventSource.removeEventListener("background", onBackground);
      eventSource.removeEventListener("permission_request", onPermissionRequest);
      eventSource.removeEventListener("turn_end", onTurnEnd);
      eventSource.removeEventListener("compact", onCompact);
      eventSource.removeEventListener("error", onError);
    };
  }, [
    eventSource,
    refetchAfterTurn,
    upsertPermissionRequest,
    appendSubagent,
    pacerEnqueueText,
    pacerEnqueueTool,
    pacerEnqueueThinking,
    pacerApplyToolResult,
    beginTurn,
    endTurn,
    pacerReset,
  ]);

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

  // Native EventSource auto-reconnects after a mid-stream drop (unlike the
  // dead-on-arrival case streamBroken handles above, which never retries on
  // its own) — but whatever frames were missed during the outage are gone
  // for good, so a live turn needs a deliberate transcript refetch the moment
  // the connection is restored to recover what the stream dropped (turn_end
  // then reconciles the live items away).
  useEffect(() => {
    if (!eventSource) return;
    const onOpen = () => {
      // Only a live turn: a revealing one already has its whole reply, and
      // the full transcript under its live items would show it twice.
      if (turnPhaseRef.current !== "live") return;
      void routerClient.getTranscript(sessionId).then(setMessages);
      refetchSession();
    };
    eventSource.addEventListener("open", onOpen);
    return () => eventSource.removeEventListener("open", onOpen);
  }, [eventSource, routerClient, sessionId, refetchSession]);

  // Sticky "a turn was in flight since we last knew the tab was visible"
  // flag. Backgrounded tabs throttle timers/connections, so a turn that
  // starts and finishes entirely while hidden can leave turn_end unhandled —
  // this catches that the moment the user looks back, rather than depending
  // on refocus to be an accidental refetch trigger (the exact bug reported).
  // Cleared once reconciled while visible; left set if the turn is still
  // running so a later visibility toggle checks again.
  const turnWasLiveRef = useRef(false);
  useEffect(() => {
    if (turnPhase !== "idle") {
      turnWasLiveRef.current = true;
    } else if (document.visibilityState === "visible") {
      turnWasLiveRef.current = false;
    }
  }, [turnPhase]);

  useEffect(() => {
    const onVisible = () => {
      if (document.visibilityState !== "visible" || !turnWasLiveRef.current) return;
      // A revealing turn already ended and has its whole reply — the full
      // transcript under its live items would show it twice. Its own
      // reconcile follows the reveal.
      if (turnPhaseRef.current === "revealing") return;
      turnWasLiveRef.current = turnPhaseRef.current !== "idle";
      void routerClient.getTranscript(sessionId).then(setMessages);
      refetchSession();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => document.removeEventListener("visibilitychange", onVisible);
  }, [routerClient, sessionId, refetchSession]);

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
  const dockLive = dockParent ? subagentStreams.get(dockParent) : undefined;
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
  // stream, so only now, once per conversation. Live frames win for any
  // subagent they've already reached (as at open).
  const needStreamReplay = dockFile?.available === false;
  const streamReplayedFor = useRef<string>(undefined);
  useEffect(() => {
    if (!needStreamReplay || streamReplayedFor.current === sessionId) return;
    let cancelled = false;
    getAllEvents(routerClient, { sessionId, types: SUBAGENT_STREAM_EVENT_TYPES })
      .then((envelopes) => {
        if (cancelled) return;
        streamReplayedFor.current = sessionId;
        const h = foldHistoryEvents(envelopes);
        setSubagentStreams((live) => new Map([...h.subagentStreams, ...live]));
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
  // view via SessionTerminalBridge on the same stream. The paced items are the
  // live turn's content.
  const inChatTurn = activeMode === "chat" && turnPhase === "live";
  // A revealing turn keeps its live items on screen until the reveal drains.
  const liveItems = inChatTurn || (activeMode === "chat" && turnPhase === "revealing") ? pacer.items : [];
  const lastLiveItem = liveItems[liveItems.length - 1];
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
    : lastLiveItem?.kind === "tool" && !lastLiveItem.tool.result
      ? { tool: lastLiveItem.tool }
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
      const stream = model.toolUseId ? subagentStreams.get(model.toolUseId) : undefined;
      const last = stream && stream.length > 0 ? stream[stream.length - 1] : undefined;
      const op =
        last?.type === "tool"
          ? toolTitle(last.tool, linkName)
          : last?.type === "thinking"
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
      for (const m of foldToolResults(messages)) {
        if (m.role === "tool" && m.tool?.id === dockTarget.toolId) {
          tool = m.tool;
          toolTimestamp = m.timestamp;
        }
      }
      for (const item of pacer.items) {
        if (item.kind === "tool" && item.tool.id === dockTarget.toolId) tool = item.tool;
      }
    }
    dockView = { type: "tool", tool, timestamp: toolTimestamp };
  } else if (dockTarget?.type === "subagent") {
    dockView = {
      type: "subagent",
      parentToolUseId: dockTarget.parentToolUseId,
      title: dockModel?.description ?? dockTarget.title,
      subtype: dockTarget.subtype ?? dockModel?.subagentType,
      items: dockFileItems ?? subagentStreams.get(dockTarget.parentToolUseId) ?? [],
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
  // the harness failed — a retry is an ordinary send of the same text.
  const noteSendStart = () => {
    if (session.activity !== "active") setWaking(true);
    if (turnPhaseRef.current !== "live") beginTurn();
  };
  const noteSendError = (msg: string) => {
    setWaking(false);
    setSendError(msg);
  };
  const noteQueued = (q: QueuedMessage) => {
    setWaking(false);
    setQueuedMessages((cur) => (cur.some((m) => m.id === q.id) ? cur : [...cur, q]));
  };
  const noteSent = (text: string) => {
    // A send against a dormant session wakes it (Router:
    // handleSessionMessages) synchronously within the POST's own
    // request/response cycle — for a fast reply, the whole turn (including
    // turn_end) can complete before this mount's /output stream (opened
    // before the child existed, permanently dead per the WHATWG EventSource
    // spec — see streamBroken above) ever reconnects. Piggyback the same
    // bounded refetch turn_end normally triggers, rather than depending on a
    // stream that's known-unreliable for this specific turn.
    const wasDormant = session.activity !== "active";
    setWaking(false);
    setMessages((m) => [...m, { role: "user", text, timestamp: new Date().toISOString() }]);
    if (wasDormant) void refetchAfterTurn();
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
      else noteSent(text);
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
              <div className={styles.stream} ref={stick.ref} onScroll={stick.onScroll} data-testid="conversation-stream">
                {provisioning && (
                  <div className={styles.provisioningWrap}>
                    <ProvisioningStrip sessionId={sessionId} onDone={onProvisioningDone} />
                  </div>
                )}
                {messages.length === 0 && worktree.cards.length === 0 && !inChatTurn && liveItems.length === 0 ? (
                  <>
                    <div className={styles.emptyTranscript}>No conversation yet</div>
                    {/* No rows to anchor to — every card is a fallback one. */}
                    {livePermissionRequests.map((req) => (
                      <PermissionCard key={req.id} request={req} onResolve={resolvePermission} />
                    ))}
                  </>
                ) : (
                  <ChatRenderer
                    messages={messages}
                    links={links}
                    viewedSessionId={sessionId}
                    attachmentUrl={(id, variant) =>
                      routerClient.attachmentUrl({ session: sessionId }, id, variant)
                    }
                    liveItems={liveItems}
                    turnActive={inChatTurn}
                    foldNewCode={wordsPerMinute !== null}
                    thinkingHistory={thinkingHistory}
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
                      since={agentSince}
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
                      <Chip tone="info" onClick={pacer.revealAll}>
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
