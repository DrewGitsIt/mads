import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Button } from "../components/Button/Button";
import { Card } from "../components/Card/Card";
import { Toast } from "../components/Toast/Toast";
import type { RouterClient, SessionEventSource } from "../router/client";
import { CommitRefusedError, TransportSwitchError } from "../router/client";
import { subscribeToEvents } from "../router/events";
import { contextWindowForModel } from "../router/modelDisplay";
import {
  SESSION_SHELL_REFETCH_EVENT_TYPES,
  WORKSPACE_SESSION_EVENT_TYPES,
} from "../router/sessionEventTypes";
import type {
  AgentState,
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
import { ConversationHeader } from "./Header/ConversationHeader";
import { useCheckoutBranch } from "./Header/useCheckoutBranch";
import { useAgentIndex } from "../router/useAgentIndex";
import { CompactionStatus } from "./CompactionStatus";
import { ChatRenderer } from "./ChatRenderer/ChatRenderer";
import { WorkingIndicator } from "./ChatRenderer/WorkingIndicator";
import { ApprovalJump } from "./ApprovalJump/ApprovalJump";
import type { PermissionReveal } from "./ApprovalJump/permissionReveal";
import { ActivityDock, type DockView } from "./activity/ActivityDock";
import { Substrip, type SubstripEntry } from "./activity/Substrip";
import {
  appendSubagentItem,
  applyTaskEvent,
  CONVERSATION_EVENT_TYPES,
  finishRunningTasks,
  foldHistoryEvents,
  replayedCompactStatus,
  resumeRunningTasks,
  tasksByToolUse,
  type ConversationHistory,
  type SubStreamItem,
  type TaskModel,
} from "./activity/conversationItems";
import { formatTokens, toolTitle } from "./activity/toolDisplay";
import { useStreamPacer } from "./streaming/useStreamPacer";
import { streamSpeedToCharsPerSec, useStreamSpeed } from "./streaming/streamSpeed";
import { PermissionCard } from "./ChatRenderer/PermissionCard";
import { TerminalView, type TerminalViewHandle } from "./TerminalRenderer/TerminalView";
import { TerminalComposer, formatBracketedPaste } from "./TerminalRenderer/TerminalComposer";
import { SessionSummaryCard } from "./SummaryCard/SessionSummaryCard";
import { Composer } from "./Composer/Composer";
import { ProvisioningStrip } from "./ProvisioningStrip";
import { ShellDock } from "./ShellDock/ShellDock";
import { ShellBridge, type ShellDockBridge } from "./ShellDock/shellBridge";
import { getShellDockState, setShellDockState, useShellDockState } from "./ShellDock/dockState";
import { CommitDialog } from "./Worktree/CommitDialog";
import { useCommitSettings } from "./Worktree/useCommitSettings";
import { useWorktreeCards } from "./Worktree/useWorktreeCards";
import { SHELL_TOGGLE_EVENT } from "./shellToggleSignal";
import { STREAM_REATTACH_EVENT, type StreamReattachDetail } from "./streamReattachSignal";
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
type TurnPhase = "idle" | "live";

/**
 * Top-level Conversation tab (MADS-UX §3.3 / W6.3). Composes the header,
 * the chat/terminal display-mode switch, the inactive-session summary
 * card, the composer, and the docked Shell — see HANDOFF.md §8 for the
 * demo-mount rationale (no Session Workspace shell exists yet to register
 * this into).
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
  const [terminalComposerOpen, setTerminalComposerOpen] = useState(false);
  const terminalViewRef = useRef<TerminalViewHandle>(null);
  const [eventSource, setEventSource] = useState<SessionEventSource | null>(null);
  const [sendError, setSendError] = useState<string>();
  const [commandToast, setCommandToast] = useState<{ tone: "ok" | "bad"; message: string }>();
  const [waking, setWaking] = useState(false);
  const [turnPhase, setTurnPhase] = useState<TurnPhase>("idle");
  // session.workspace is a reference (id or name), not the name the header's
  // subline prints — resolved here, off the session this tab already holds, and
  // kept live by the hook's workspace.* subscription.
  const sessionWorkspace = useWorkspaceForRef(routerClient, session?.workspace);
  const agents = useAgentIndex(routerClient);
  const checkoutBranch = useCheckoutBranch(routerClient, session);
  // Paces the live turn's reveal (typewriter smoothing, off by default). Owns
  // the ordered items; enqueue/finish/reset are stable across renders.
  const pacer = useStreamPacer(streamSpeedToCharsPerSec(useStreamSpeed(routerClient)));
  const {
    enqueueText: pacerEnqueueText,
    enqueueTool: pacerEnqueueTool,
    enqueueThinking: pacerEnqueueThinking,
    applyToolResult: pacerApplyToolResult,
    finish: pacerFinish,
    reset: pacerReset,
  } = pacer;
  // Last completed turn's usage, for the composer context meter. Populated from
  // the stream's turn_end frame (the only place usage is reported); undefined
  // until this session view sees a turn finish.
  const [lastUsage, setLastUsage] = useState<Usage>();
  // Native compaction progress (Runner's resume-above-threshold trigger,
  // services/session/internal/runner/compact.go) — surfaced explicitly since
  // headless -p mode has no interactive "resume from summary" prompt.
  // Cleared on session switch same as everything else reset in the mount
  // effect below; not persisted, since it's meaningful only while this tab
  // is watching the live stream.
  const [compactStatus, setCompactStatus] = useState<CompactStatus>();
  // "Not now" on the compact-suggestion banner — sticky per mount, so
  // declining once quiets the banner until the session is next opened. The
  // server only force-compacts near the window limit (compact.go's ~85%
  // threshold); everything below that is this user choice, replicating the
  // resume options interactive claude-code offers that headless -p lacks.
  const [compactSuggestionDismissed, setCompactSuggestionDismissed] = useState(false);
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

  // --- Commit and Land (docs/SESSION-WORKTREES-DESIGN.md D4–D6) ---
  // Result cards from the event log plus the actions that make them; the
  // header control reads its state off the session record's counts.
  const worktree = useWorktreeCards(routerClient, sessionId);
  const { commitAll, land } = worktree;
  const commitSettings = useCommitSettings(routerClient);
  // The Commit dialog, optionally opened on a one-click Commit's refusal
  // (no Fast Model after all, a failing hook) so the user can see why and
  // write the message themselves.
  const [commitDialog, setCommitDialog] = useState<{ initialError?: CommitError } | null>(null);
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
    setCompactSuggestionDismissed(false);
    setAgentSince(undefined);
    setPendingReplay(null);
    setQueuedMessages([]);
    setToolStatuses(new Map());
    setTasks(new Map());
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
          // Seeds the composer meter from the session record's last-known
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
    void routerClient
      .getEvents({ sessionId, types: CONVERSATION_EVENT_TYPES })
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
    if (turnPhase === "idle") setAgentSince(undefined);
  }, [turnPhase]);

  const refetchSession = useCallback(() => {
    void routerClient.getSession(sessionId).then(setSession);
  }, [routerClient, sessionId]);

  // Shared by both the header's agent/model pills and the Composer's
  // "/agent"/"/model" commands — there's no Router PATCH /sessions/:id yet,
  // so this is a local-state commit exactly like the pills already did
  // before the Composer had any use for it.
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

  // Header model pill. The composer's "/model" goes through
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

  // Agent and name stay local-only: unlike model and trust, neither has a
  // live-apply endpoint yet (the agent's system prompt is spawn-time too, so
  // the pill is cosmetic until one exists — see HANDOFF.md).
  const handleUpdateSession = useCallback(
    async (patch: Partial<Pick<Session, "name" | "agent" | "model">>) => {
      if (patch.model !== undefined) return applyModel(patch.model);
      setSession((s) => (s ? { ...s, ...patch } : s));
    },
    [applyModel],
  );

  // Header trust pill → POST /sessions/{id}/trust. Unlike the local-only
  // pills above this one persists (and the Router bounces a live child so
  // the new level actually applies — trust is spawn-time-only in the
  // runner); the toast says so instead of leaving the restart mysterious.
  const handleChangeTrust = useCallback(
    (trust: NonNullable<Session["trust"]>) => {
      void routerClient
        .setSessionTrust(sessionId, trust)
        .then((updated) => {
          // effective_trust rides along: the Router recomputes it for the
          // new level (auto_review on a provider with no reviewer → limited),
          // and a stale one would keep — or miss — the header's fallback.
          setSession((s) =>
            s ? { ...s, trust: updated.trust, effective_trust: updated.effective_trust } : s,
          );
          setCommandToast({ tone: "ok", message: `Trust set to ${trust} — agent restarted to apply it.` });
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

  // Header Commit…: one click with "Write commit messages for me" on and a
  // Fast Model to write them, else the dialog.
  const handleCommit = useCallback(() => {
    if (!(commitSettings.autoCommitMessages && commitSettings.fastModel)) {
      setCommitDialog({});
      return;
    }
    commitAll().catch((err: unknown) => {
      if (err instanceof CommitRefusedError) setCommitDialog({ initialError: err.body });
      else
        setCommandToast({
          tone: "bad",
          message: err instanceof Error ? err.message : "Couldn't commit.",
        });
    });
  }, [commitSettings, commitAll]);

  // Header Land / a card's "Land again". The outcome is a card either way;
  // only an unexpected failure (no card coming) is a toast.
  const handleLand = useCallback(() => {
    land().catch((err: unknown) => {
      setCommandToast({
        tone: "bad",
        message: err instanceof Error ? err.message : "Couldn't land.",
      });
    });
  }, [land]);

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
  const refetchAfterTurn = useCallback(async () => {
    const baseline = messagesRef.current.length;
    for (let attempt = 0; attempt < 2; attempt++) {
      try {
        const fresh = await routerClient.getTranscript(sessionId);
        if (fresh.length > baseline || attempt === 1) {
          setMessages(fresh);
          return;
        }
      } catch {
        if (attempt === 1) return;
      }
      await new Promise((resolve) => setTimeout(resolve, 300));
    }
  }, [routerClient, sessionId]);

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
    pacerFinish(() => {
      void refetchAfterTurn().finally(() => {
        setTurnPhase("idle");
        pacerReset();
      });
    });
  }, [routerClient, sessionId, pacerFinish, refetchAfterTurn, pacerReset]);

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
  useEffect(() => {
    return subscribeToEvents({ sessionId, types: ["session.turn_ended", "session.agent_state"] }, (envelope) => {
      if (envelope.type === "session.agent_state") {
        const state = envelope.payload as Partial<AgentState> | undefined;
        if (state?.state === "working") {
          setTurnPhase("live");
          setAgentSince(state.since || envelope.timestamp);
          // The stream's own tool/thinking frames keep the ticker current;
          // the server label only fills it when none has arrived yet.
          if (state.label) setCurrentActivity((a) => a ?? state.label);
          return;
        }
        if (state?.state !== "idle") return;
      }
      if (turnPhaseRef.current !== "idle") {
        setCurrentActivity(undefined);
        setTasks((m) => finishRunningTasks(m));
        pacerFinish(() => {
          void refetchAfterTurn().finally(() => {
            setTurnPhase("idle");
            pacerReset();
          });
        });
      } else {
        void refetchAfterTurn();
      }
    });
  }, [sessionId, pacerFinish, refetchAfterTurn, pacerReset]);

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
        setMessages((m) => [...m, { role: "user", text, timestamp: new Date().toISOString() }]);
        setTurnPhase((p) => (p === "idle" ? "live" : p));
      }
      if (change.failed) {
        setSendError(`Couldn't send your queued message: ${change.error ?? "unknown error"}`);
      }
    });
  }, [sessionId]);

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

  const handleCompactNow = useCallback(async () => {
    // Optimistic "Compacting…" so the click lands visibly even before the
    // provider's own compact/start stream event arrives (the Router may
    // first have to wake a dormant session); the real done/failed event
    // overwrites it.
    setCompactSuggestionDismissed(true);
    setCompactStatus({ phase: "start" });
    try {
      await routerClient.compactSession(sessionId);
    } catch (err) {
      setCompactStatus(undefined);
      setSendError(err instanceof Error ? err.message : "Failed to compact the conversation.");
    }
  }, [routerClient, sessionId]);

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
      setTurnPhase("live");
      if (parsed.parent_tool_use_id) {
        appendSubagent(parsed.parent_tool_use_id, { kind: "text", text: parsed.text });
        return;
      }
      pacerEnqueueText(parsed.text);
    };
    const onTool = (ev: MessageEvent<string>) => {
      const parsed = JSON.parse(ev.data) as StreamEvent;
      if (parsed.kind !== "tool_use") return;
      setTurnPhase("live");
      if (parsed.parent_tool_use_id) {
        appendSubagent(parsed.parent_tool_use_id, { kind: "tool_use", tool: parsed.tool });
        return;
      }
      pacerEnqueueTool(parsed.tool, new Date().toISOString());
      setCurrentActivity(toolTitle(parsed.tool));
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
      setTurnPhase("live");
      if (parsed.parent_tool_use_id) {
        if (parsed.text) {
          appendSubagent(parsed.parent_tool_use_id, { kind: "thinking", text: parsed.text });
        }
        return;
      }
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
      setTurnPhase("live");
      setTasks((m) => applyTaskEvent(m, parsed.task, true));
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
      // it feeds the composer context meter.
      if (typeof ev.data === "string") {
        try {
          const parsed = JSON.parse(ev.data) as StreamEvent;
          if (parsed.kind === "turn_end" && parsed.usage) {
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
      // Drain whatever the pacer still has buffered (so a paced turn finishes
      // typing smoothly), then reconcile once with the persisted transcript and
      // go idle — frozen-visible throughout, so there's never a flash to empty.
      pacerFinish(() => {
        void refetchAfterTurn().finally(() => {
          setTurnPhase("idle");
          pacerReset();
        });
      });
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
    pacerFinish,
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
      if (turnPhaseRef.current === "idle") return;
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
      turnWasLiveRef.current = turnPhaseRef.current !== "idle";
      void routerClient.getTranscript(sessionId).then(setMessages);
      refetchSession();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => document.removeEventListener("visibilitychange", onVisible);
  }, [routerClient, sessionId, refetchSession]);

  // Conversation scroll: auto-follow the tail as the agent streams, but yield
  // control the instant the user scrolls up — re-engaging only once they're
  // back at the bottom. stickToBottom is a ref (not state) so updating it from
  // the scroll handler never itself triggers a render.
  const streamRef = useRef<HTMLDivElement>(null);
  const stickToBottom = useRef(true);
  // The approval jump's reach: the whole tab (stream + dock) for finding
  // cards, the pinned cluster for what the composer covers. The reveal is
  // its latest click, published to collapsed groups (permissionReveal.ts).
  const tabRef = useRef<HTMLDivElement>(null);
  const pinnedRef = useRef<HTMLDivElement>(null);
  const [permissionReveal, setPermissionReveal] = useState<PermissionReveal | null>(null);
  const revealPermission = useCallback((id: string) => {
    // Jumping up the history is the user leaving the tail — don't let the
    // next streamed frame snap back before the smooth scroll's first event.
    stickToBottom.current = false;
    setPermissionReveal((cur) => ({ id, nonce: (cur?.nonce ?? 0) + 1 }));
  }, []);

  const handleStreamScroll = useCallback(() => {
    const el = streamRef.current;
    if (!el) return;
    const distanceFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
    stickToBottom.current = distanceFromBottom < 48;
  }, []);

  useLayoutEffect(() => {
    const el = streamRef.current;
    if (el && stickToBottom.current) el.scrollTop = el.scrollHeight;
  }, [messages, pacer.items, turnPhase]);

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
      el.scrollTo({ top: target, behavior: "smooth" });
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

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
  const showHistory = transport.status !== "idle" || transport.mode === "chat";
  // Never shown mid-switch/terminal — terminal mode already has its own live
  // view via SessionTerminalBridge on the same stream. The paced items are the
  // live turn's content.
  const inChatTurn = activeMode === "chat" && turnPhase === "live";
  const liveItems = inChatTurn ? pacer.items : [];
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
  // Never carry a stale open overlay across a switch back to chat and later
  // back to terminal — a bare `!showHistory` gate on `open` below would hide
  // it visually but leave the state true, popping it back open unprompted.
  if (terminalComposerOpen && activeMode !== "terminal") {
    setTerminalComposerOpen(false);
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
  const tasksByToolUseMap = tasksByToolUse(tasks);
  // The approval jump's "· <subagent>" — named as its task card titles it.
  const subagentName = (req: PermissionRequest) => {
    if (!req.parent_tool_use_id) return undefined;
    const model = tasksByToolUseMap.get(req.parent_tool_use_id);
    return model?.description ?? model?.subagentType ?? "subagent";
  };
  const substripEntries: SubstripEntry[] = [...tasks.values()]
    .filter((t) => t.running)
    .map((model) => {
      const stream = model.toolUseId ? subagentStreams.get(model.toolUseId) : undefined;
      const last = stream && stream.length > 0 ? stream[stream.length - 1] : undefined;
      const op =
        last?.type === "tool"
          ? toolTitle(last.tool)
          : last?.type === "thinking"
            ? "Thinking…"
            : model.lastToolName;
      return { model, op };
    });
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
    const model = [...tasks.values()].find((t) => t.toolUseId === dockTarget.parentToolUseId);
    dockView = {
      type: "subagent",
      parentToolUseId: dockTarget.parentToolUseId,
      title: model?.description ?? dockTarget.title,
      subtype: dockTarget.subtype ?? model?.subagentType,
      items: subagentStreams.get(dockTarget.parentToolUseId) ?? [],
      task: model,
    };
  }
  // Compact-suggestion input, derived locally from the same usage the composer
  // meter reads (session.last_usage at mount, turn_end frames after) — no
  // extra stream event or server round-trip, and no race against the
  // Runner's own Start-time trigger, which only fires near the window limit.
  const occupancyWindow = lastUsage?.context_window || contextWindowForModel(session.model);
  // Only context_tokens (the final API call's prompt side) feeds the
  // suggestion: the summed token fields are claude-code's per-turn billing
  // aggregate (~N× real occupancy — a 3-iteration small turn reads 60%+),
  // so a proactive "compact?" off them is a false positive. The meter keeps
  // its aggregate fallback rendering for stale sessions; without trusted
  // numbers the suggestion simply stays away. Clamped at 100 like the meter
  // so the two can never contradict ("~134%" is impossible).
  const occupancyTokens = lastUsage?.context_tokens ?? 0;
  const occupancyPct =
    occupancyWindow > 0 ? Math.min(100, Math.round((occupancyTokens / occupancyWindow) * 100)) : 0;
  const showCompactSuggestion =
    activeMode === "chat" &&
    !inChatTurn &&
    !compactSuggestionDismissed &&
    !compactStatus &&
    occupancyTokens > 0 &&
    occupancyPct >= 60;

  return (
    <div className={styles.tab} ref={tabRef}>
      <ConversationHeader
        session={session}
        workspaceName={sessionWorkspace?.name ?? undefined}
        agents={agents}
        checkoutBranch={checkoutBranch}
        mode={activeMode}
        onModeChange={(m) => void handleToggle(m)}
        onUpdateSession={handleUpdateSession}
        onChangeModel={handleChangeModel}
        onChangeTrust={handleChangeTrust}
        work={{
          onCommit: handleCommit,
          onLand: handleLand,
          landing: worktree.landing,
          committing: worktree.committing,
        }}
      />

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
        <div className={styles.stream} ref={streamRef} onScroll={handleStreamScroll}>
          {showHistory ? (
            <>
              {provisioning && (
                <div className={styles.provisioningWrap}>
                  <ProvisioningStrip sessionId={sessionId} onDone={onProvisioningDone} />
                </div>
              )}
              <SessionSummaryCard session={session} />
              {messages.length === 0 && worktree.cards.length === 0 && !inChatTurn ? (
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
                  thinkingHistory={thinkingHistory}
                  toolStatuses={toolStatuses}
                  tasks={tasksByToolUseMap}
                  selectedToolId={dockTarget?.type === "tool" ? dockTarget.toolId : undefined}
                  onOpenTool={openTool}
                  onOpenTask={(toolUseId, tool) =>
                    openSubagent(
                      toolUseId,
                      tasksByToolUseMap.get(toolUseId)?.description ?? toolTitle(tool),
                      tasksByToolUseMap.get(toolUseId)?.subagentType,
                    )
                  }
                  worktreeCards={worktree.cards}
                  worktreeActions={worktreeActions}
                  permissionRequests={livePermissionRequests}
                  onResolvePermission={resolvePermission}
                  permissionReveal={permissionReveal}
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
              {showCompactSuggestion && (
                <div className={styles.transportBanner}>
                  <span>
                    This conversation is using ~{occupancyPct}% of the context window. Compacting
                    summarizes older messages to free space.
                  </span>
                  <div className={styles.transportActions}>
                    <Button variant="secondary" size="sm" onClick={() => void handleCompactNow()}>
                      Compact now
                    </Button>
                    <Button
                      variant="secondary"
                      size="sm"
                      onClick={() => setCompactSuggestionDismissed(true)}
                    >
                      Not now
                    </Button>
                  </div>
                </div>
              )}
              <CompactionStatus
                status={compactStatus}
                onDismiss={() => {
                  writeCompactDismissedAt(sessionId);
                  setCompactStatus(undefined);
                }}
              />
              {/* Pinned bottom cluster (T16): running-subagent substrip +
                  turn-long working indicator + composer stick together at
                  the bottom of the scrollable stream — none can scroll off
                  screen while the conversation is on screen. */}
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
                <Substrip
                  entries={substripEntries}
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
                <Composer
                routerClient={routerClient}
                sessionId={sessionId}
                agentId={session.agent}
                provider={session.provider}
                usage={lastUsage}
                contextWindow={lastUsage ? contextWindowForModel(session.model) : undefined}
                disabled={transport.status !== "idle" || waking}
                busy={inChatTurn}
                onStop={() => void handleInterrupt()}
                initialValue={pendingFirstMessage}
                prefill={composerPrefill}
                autoSendInitial={Boolean(pendingFirstMessage)}
                onSendStart={() => {
                  if (session.activity !== "active") setWaking(true);
                  setTurnPhase((p) => (p === "idle" ? "live" : p));
                }}
                onSendError={(msg) => {
                  setWaking(false);
                  setSendError(msg);
                }}
                onAutoSendStart={onPendingFirstMessageHandled}
                onUpdateSession={handleUpdateSession}
                onCommandExecuted={(message) => setCommandToast({ tone: "ok", message })}
                onCommandError={(message) => setCommandToast({ tone: "bad", message })}
                queued={queuedMessages}
                onQueued={(q) => {
                  setWaking(false);
                  setQueuedMessages((cur) => (cur.some((m) => m.id === q.id) ? cur : [...cur, q]));
                }}
                onCancelQueued={handleCancelQueued}
                onSent={(text) => {
                  // A send against a dormant session wakes it (Router:
                  // handleSessionMessages) synchronously within the POST's
                  // own request/response cycle — for a fast reply, the whole
                  // turn (including turn_end) can complete before this
                  // mount's /output stream (opened before the child existed,
                  // permanently dead per the WHATWG EventSource spec — see
                  // streamBroken above) ever reconnects. Piggyback the same
                  // bounded refetch turn_end normally triggers, rather than
                  // depending on a stream that's known-unreliable for this
                  // specific turn.
                  const wasDormant = session.activity !== "active";
                  setWaking(false);
                  setMessages((m) => [...m, { role: "user", text, timestamp: new Date().toISOString() }]);
                  if (wasDormant) void refetchAfterTurn();
                }}
                />
              </div>
            </>
          ) : terminalBridge ? (
            <TerminalView
              ref={terminalViewRef}
              bridge={terminalBridge}
              className={styles.terminal}
              onComposeOpen={() => setTerminalComposerOpen(true)}
            />
          ) : (
            <div className={styles.ptyUnavailable}>Connecting…</div>
          )}
        </div>

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

      {terminalBridge && (
        <TerminalComposer
          open={terminalComposerOpen}
          onClose={() => {
            setTerminalComposerOpen(false);
            terminalViewRef.current?.focus();
          }}
          onSend={(text) => {
            terminalBridge.write(sessionId, formatBracketedPaste(text));
            setTerminalComposerOpen(false);
            terminalViewRef.current?.focus();
          }}
        />
      )}

      {dockView && (
        <ActivityDock
          view={dockView}
          sessionId={sessionId}
          client={routerClient}
          statuses={toolStatuses}
          permissions={permissionsByToolUse}
          onResolvePermission={resolvePermission}
          onClose={() => setDockTarget(null)}
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
          onDismiss={() => setCommandToast(undefined)}
        />
      )}
    </div>
  );
}
