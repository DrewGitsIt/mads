import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Button } from "../components/Button/Button";
import { Card } from "../components/Card/Card";
import { Toast } from "../components/Toast/Toast";
import type { RouterClient, SessionEventSource } from "../router/client";
import { TransportSwitchError } from "../router/client";
import { subscribeToEvents } from "../router/events";
import { contextWindowForModel } from "../router/modelDisplay";
import {
  SESSION_SHELL_REFETCH_EVENT_TYPES,
  WORKSPACE_SESSION_EVENT_TYPES,
} from "../router/sessionEventTypes";
import type {
  CompactStatus,
  PermissionRequest,
  Session,
  StreamEvent,
  ToolUse,
  TranscriptMessage,
  Usage,
} from "../router/types";
import { useSessionLinks } from "../features/sessions/state/useSessionLinks";
import type { PtyBridge } from "./TerminalRenderer/ptyBridge";
import { SessionTerminalBridge } from "./TerminalRenderer/sessionTerminalBridge";
import { ConversationHeader } from "./Header/ConversationHeader";
import { CompactionStatus } from "./CompactionStatus";
import { ChatRenderer } from "./ChatRenderer/ChatRenderer";
import { useStreamPacer } from "./streaming/useStreamPacer";
import { streamSpeedToCharsPerSec, useStreamSpeed } from "./streaming/streamSpeed";
import { PermissionCard } from "./ChatRenderer/PermissionCard";
import { TerminalView, type TerminalViewHandle } from "./TerminalRenderer/TerminalView";
import { TerminalComposer, formatBracketedPaste } from "./TerminalRenderer/TerminalComposer";
import { SessionSummaryCard } from "./SummaryCard/SessionSummaryCard";
import { Composer } from "./Composer/Composer";
import { ProvisioningStrip } from "./ProvisioningStrip";
import { ShellDock } from "./ShellDock/ShellDock";
import { SHELL_TOGGLE_EVENT } from "./shellToggleSignal";
import styles from "./ConversationTab.module.css";

export interface ConversationTabProps {
  sessionId: string;
  routerClient: RouterClient;
  /**
   * Undefined outside Electron (e.g. `npm run dev`'s renderer-only server,
   * or any vitest environment) — terminal mode and the Shell dock both
   * degrade to a plain notice rather than importing node-pty (HANDOFF.md
   * §5). The same bridge is used for both; each spawns its own pty id, so
   * they never actually share a pty.
   */
  ptyBridge?: PtyBridge;
  /** Shows the ProvisioningStrip until the session goes active — set for a session just created from the Launchpad. */
  provisioning?: boolean;
  onProvisioningDone?: () => void;
  /** A goal/message to auto-send through the Composer once, on mount — set for a session just created from the Launchpad. */
  pendingFirstMessage?: string;
  onPendingFirstMessageHandled?: () => void;
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
  ptyBridge,
  provisioning,
  onProvisioningDone,
  pendingFirstMessage,
  onPendingFirstMessageHandled,
}: ConversationTabProps) {
  const [session, setSession] = useState<Session | null>(null);
  const [messages, setMessages] = useState<TranscriptMessage[]>([]);
  const [loadError, setLoadError] = useState(false);
  const [retryToken, setRetryToken] = useState(0);
  const [transport, setTransport] = useState<TransportState>({ status: "idle", mode: "chat" });
  const [shellOpen, setShellOpen] = useState(false);
  const [terminalComposerOpen, setTerminalComposerOpen] = useState(false);
  const terminalViewRef = useRef<TerminalViewHandle>(null);
  const [eventSource, setEventSource] = useState<SessionEventSource | null>(null);
  const [sendError, setSendError] = useState<string>();
  const [commandToast, setCommandToast] = useState<{ tone: "ok" | "bad"; message: string }>();
  const [waking, setWaking] = useState(false);
  const [turnPhase, setTurnPhase] = useState<TurnPhase>("idle");
  const [turnBlocked, setTurnBlocked] = useState<string>();
  // Paces the live turn's reveal (typewriter smoothing, off by default). Owns
  // the ordered items; enqueue/finish/reset are stable across renders.
  const pacer = useStreamPacer(streamSpeedToCharsPerSec(useStreamSpeed(routerClient)));
  const {
    enqueueText: pacerEnqueueText,
    enqueueTool: pacerEnqueueTool,
    applyToolResult: pacerApplyToolResult,
    finish: pacerFinish,
    reset: pacerReset,
  } = pacer;
  // Last completed turn's usage, for the header context meter. Populated from
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

  useEffect(() => {
    const handler = () => setShellOpen((x) => !x);
    window.addEventListener(SHELL_TOGGLE_EVENT, handler);
    return () => window.removeEventListener(SHELL_TOGGLE_EVENT, handler);
  }, []);

  useEffect(() => {
    let cancelled = false;
    setLoadError(false);
    setSession(null);
    setMessages([]);
    setTurnPhase("idle");
    setTurnBlocked(undefined);
    pacerReset();
    setPermissionRequests(new Map());
    setLastUsage(undefined);
    setCompactStatus(undefined);
    // Reset to the chat default up front — the mode lookup below is async,
    // so a stale mode carried over from whatever session was mounted before
    // this one must never leak into this session's initial render.
    setTransport({ status: "idle", mode: "chat" });
    void routerClient
      .getSession(sessionId)
      .then((s) => {
        if (!cancelled) {
          setSession(s);
          // Seeds the header meter from the session record's last-known
          // usage at wake time — before this mount has seen a turn_end of
          // its own (services/session/internal/runner's persistUsage).
          if (s.last_usage) setLastUsage(s.last_usage);
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
      .getPermissionRequests(sessionId)
      .then((reqs) => {
        if (cancelled) return;
        setPermissionRequests(new Map(reqs.map((r) => [r.id, r])));
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

  const refetchSession = useCallback(() => {
    void routerClient.getSession(sessionId).then(setSession);
  }, [routerClient, sessionId]);

  // Shared by both the header's agent/model pills and the Composer's
  // "/agent"/"/model" commands — there's no Router PATCH /sessions/:id yet,
  // so this is a local-state commit exactly like the pills already did
  // before the Composer had any use for it.
  const handleUpdateSession = useCallback(
    (patch: Partial<Pick<Session, "name" | "agent" | "model">>) => {
      setSession((s) => (s ? { ...s, ...patch } : s));
    },
    [],
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
    pacerFinish(() => {
      void refetchAfterTurn().finally(() => {
        setTurnPhase("idle");
        setTurnBlocked(undefined);
        pacerReset();
      });
    });
  }, [routerClient, sessionId, pacerFinish, refetchAfterTurn, pacerReset]);

  // Consumes the same live output stream SessionTerminalBridge rides for
  // terminal mode (set up above) — chat mode folds its text/tool_use/
  // turn_end/error frames into the turn-state machine instead of opening a
  // second stream.
  useEffect(() => {
    if (!eventSource) return;
    const onText = (ev: MessageEvent<string>) => {
      const parsed = JSON.parse(ev.data) as StreamEvent;
      if (parsed.kind !== "text") return;
      setTurnPhase("live");
      pacerEnqueueText(parsed.text);
    };
    const onTool = (ev: MessageEvent<string>) => {
      const parsed = JSON.parse(ev.data) as StreamEvent;
      if (parsed.kind !== "tool_use") return;
      setTurnPhase("live");
      pacerEnqueueTool(parsed.tool);
    };
    const onToolResult = (ev: MessageEvent<string>) => {
      const parsed = JSON.parse(ev.data) as StreamEvent;
      if (parsed.kind !== "tool_result") return;
      // Attach the result to its live tool card (matched by id) so the card
      // shows its outcome as it arrives — never a silent no-op, and an error
      // (e.g. a permission denial) also raises the blocked banner even if the
      // process then stalls with no further wire activity.
      pacerApplyToolResult(parsed.tool?.id, { text: parsed.text, is_error: parsed.is_error });
      if (parsed.is_error) {
        setTurnBlocked("blocked awaiting permission — switch to terminal or raise trust");
      }
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
      // it feeds the header context meter.
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
      // Drain whatever the pacer still has buffered (so a paced turn finishes
      // typing smoothly), then reconcile once with the persisted transcript and
      // go idle — frozen-visible throughout, so there's never a flash to empty.
      pacerFinish(() => {
        void refetchAfterTurn().finally(() => {
          setTurnPhase("idle");
          setTurnBlocked(undefined);
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
      setTurnBlocked(undefined);
      pacerReset();
      setSendError(parsed.kind === "error" ? parsed.text : "The agent hit an error.");
    };
    eventSource.addEventListener("text", onText);
    eventSource.addEventListener("tool_use", onTool);
    eventSource.addEventListener("tool_result", onToolResult);
    eventSource.addEventListener("permission_request", onPermissionRequest);
    eventSource.addEventListener("turn_end", onTurnEnd);
    eventSource.addEventListener("compact", onCompact);
    eventSource.addEventListener("error", onError);
    return () => {
      eventSource.removeEventListener("text", onText);
      eventSource.removeEventListener("tool_use", onTool);
      eventSource.removeEventListener("tool_result", onToolResult);
      eventSource.removeEventListener("permission_request", onPermissionRequest);
      eventSource.removeEventListener("turn_end", onTurnEnd);
      eventSource.removeEventListener("compact", onCompact);
      eventSource.removeEventListener("error", onError);
    };
  }, [
    eventSource,
    refetchAfterTurn,
    upsertPermissionRequest,
    pacerEnqueueText,
    pacerEnqueueTool,
    pacerApplyToolResult,
    pacerFinish,
    pacerReset,
  ]);

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
  // Trailing "working" affordance — only when the tail isn't itself visibly
  // streaming: pre-first-output, a tool awaiting its result, or a stall.
  const working: { tool?: ToolUse; blocked?: string } | false = !inChatTurn
    ? false
    : turnBlocked
      ? { blocked: turnBlocked }
      : liveItems.length === 0
        ? {}
        : lastLiveItem?.kind === "tool" && !lastLiveItem.tool.result
          ? { tool: lastLiveItem.tool }
          : false;
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

  return (
    <div className={styles.tab}>
      <ConversationHeader
        session={session}
        usage={lastUsage}
        contextWindow={lastUsage ? contextWindowForModel(session.model) : undefined}
        mode={activeMode}
        onModeChange={(m) => void handleToggle(m)}
        onUpdateSession={handleUpdateSession}
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
              {messages.length === 0 && !inChatTurn ? (
                <div className={styles.emptyTranscript}>No conversation yet</div>
              ) : (
                <ChatRenderer
                  messages={messages}
                  links={links}
                  viewedSessionId={sessionId}
                  liveItems={liveItems}
                  working={working}
                />
              )}
              {livePermissionRequests.map((req) => (
                <PermissionCard
                  key={req.id}
                  request={req}
                  onResolve={(id, decision) =>
                    routerClient.resolvePermissionRequest(sessionId, id, decision)
                  }
                />
              ))}
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
              <CompactionStatus status={compactStatus} />
              <Composer
                routerClient={routerClient}
                sessionId={sessionId}
                agentId={session.agent}
                provider={session.provider}
                disabled={transport.status !== "idle" || waking}
                busy={inChatTurn}
                onStop={() => void handleInterrupt()}
                initialValue={pendingFirstMessage}
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
                  setMessages((m) => [...m, { role: "user", text }]);
                  if (wasDormant) void refetchAfterTurn();
                }}
              />
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
          (ptyBridge ? (
            <ShellDock bridge={ptyBridge} cwd={session.cwd ?? session.worktree_dir} />
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
