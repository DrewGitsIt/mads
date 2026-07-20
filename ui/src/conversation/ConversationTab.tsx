import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button } from "../components/Button/Button";
import { Card } from "../components/Card/Card";
import { Chip } from "../components/Chip/Chip";
import { Toast } from "../components/Toast/Toast";
import type { RouterClient, SessionEventSource } from "../router/client";
import { TransportSwitchError } from "../router/client";
import { subscribeToEvents } from "../router/events";
import {
  SESSION_SHELL_REFETCH_EVENT_TYPES,
  WORKSPACE_SESSION_EVENT_TYPES,
} from "../router/sessionEventTypes";
import type { Session, StreamEvent, ToolUse, TranscriptMessage } from "../router/types";
import { useSessionLinks } from "../features/sessions/state/useSessionLinks";
import type { PtyBridge } from "./TerminalRenderer/ptyBridge";
import { SessionTerminalBridge } from "./TerminalRenderer/sessionTerminalBridge";
import { ConversationHeader } from "./Header/ConversationHeader";
import { ChatRenderer } from "./ChatRenderer/ChatRenderer";
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
type TurnState =
  | { phase: "idle" }
  // turn started, no text chunk yet this turn; `blocked` is set when the
  // provider reports the pending tool stalled awaiting permission rather
  // than completing (a headless transport has no surface to answer the
  // prompt — see providers/claudecode's README, "Defense in depth")
  | { phase: "waiting"; tool?: ToolUse; blocked?: string }
  | { phase: "streaming"; text: string }; // >=1 text chunk arrived this turn

// Cadence for the mid-turn transcript refresh below — modest enough that a
// live turn's tool cards/paragraphs settle in well under a second of visible
// lag, without turning every turn into a request storm.
const TURN_POLL_INTERVAL_MS = 1500;

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
  const [turn, setTurn] = useState<TurnState>({ phase: "idle" });
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
    setTurn({ phase: "idle" });
    // Reset to the chat default up front — the mode lookup below is async,
    // so a stale mode carried over from whatever session was mounted before
    // this one must never leak into this session's initial render.
    setTransport({ status: "idle", mode: "chat" });
    void routerClient
      .getSession(sessionId)
      .then((s) => {
        if (!cancelled) setSession(s);
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
  }, [routerClient, sessionId, retryToken]);

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
  const turnPhaseRef = useRef(turn.phase);
  turnPhaseRef.current = turn.phase;

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

  // Consumes the same live output stream SessionTerminalBridge rides for
  // terminal mode (set up above) — chat mode folds its text/tool_use/
  // turn_end/error frames into the turn-state machine instead of opening a
  // second stream.
  useEffect(() => {
    if (!eventSource) return;
    const onText = (ev: MessageEvent<string>) => {
      const parsed = JSON.parse(ev.data) as StreamEvent;
      if (parsed.kind !== "text") return;
      setTurn((t) => ({
        phase: "streaming",
        text: (t.phase === "streaming" ? t.text : "") + parsed.text,
      }));
    };
    const onTool = (ev: MessageEvent<string>) => {
      const parsed = JSON.parse(ev.data) as StreamEvent;
      if (parsed.kind !== "tool_use") return;
      setTurn((t) => (t.phase === "streaming" ? t : { phase: "waiting", tool: parsed.tool }));
    };
    const onToolResult = (ev: MessageEvent<string>) => {
      const parsed = JSON.parse(ev.data) as StreamEvent;
      if (parsed.kind !== "tool_result" || !parsed.is_error) return;
      // Never a silent no-op: a failed/denied tool call surfaces here even
      // if the process is otherwise stalled with no further wire activity.
      setTurn((t) => ({
        phase: "waiting",
        tool: t.phase === "waiting" ? t.tool : undefined,
        blocked: "blocked awaiting permission — switch to terminal or raise trust",
      }));
    };
    const onTurnEnd = () => {
      // Only the pre-first-chunk indicator clears immediately — a streamed
      // bubble stays frozen-visible until the persisted refetch replaces it,
      // so there's never a flash back to empty.
      setTurn((t) => (t.phase === "streaming" ? t : { phase: "idle" }));
      void refetchAfterTurn().finally(() => setTurn({ phase: "idle" }));
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
      setTurn({ phase: "idle" });
      setSendError(parsed.kind === "error" ? parsed.text : "The agent hit an error.");
    };
    eventSource.addEventListener("text", onText);
    eventSource.addEventListener("tool_use", onTool);
    eventSource.addEventListener("tool_result", onToolResult);
    eventSource.addEventListener("turn_end", onTurnEnd);
    eventSource.addEventListener("error", onError);
    return () => {
      eventSource.removeEventListener("text", onText);
      eventSource.removeEventListener("tool_use", onTool);
      eventSource.removeEventListener("tool_result", onToolResult);
      eventSource.removeEventListener("turn_end", onTurnEnd);
      eventSource.removeEventListener("error", onError);
    };
  }, [eventSource, refetchAfterTurn]);

  // While a turn is live, periodically re-fetch the persisted transcript —
  // the provider's transcript file is written continuously during a turn
  // (same premise as the pipeline drill-in's turns feed, DrillInFeed.tsx),
  // so completed paragraphs and tool cards surface well before turn_end
  // instead of piling up as one streamed blob. The streamed bubble is reset
  // on every refresh since its contents are now part of a persisted block —
  // it just shrinks to whatever text has arrived since. Stops the instant
  // the turn goes idle or this tab unmounts; never runs while idle.
  useEffect(() => {
    if (turn.phase === "idle") return;
    let cancelled = false;
    const id = window.setInterval(() => {
      void routerClient.getTranscript(sessionId).then((fresh) => {
        if (cancelled) return;
        setMessages(fresh);
        setTurn((t) => (t.phase === "streaming" ? { phase: "streaming", text: "" } : t));
      });
    }, TURN_POLL_INTERVAL_MS);
    return () => {
      cancelled = true;
      window.clearInterval(id);
    };
  }, [turn.phase, routerClient, sessionId]);

  // Native EventSource auto-reconnects after a mid-stream drop (unlike the
  // dead-on-arrival case streamBroken handles above, which never retries on
  // its own) — but whatever frames were missed during the outage are gone
  // for good, so a live turn needs a deliberate refetch the moment the
  // connection is restored, rather than waiting up to
  // TURN_POLL_INTERVAL_MS for the next scheduled poll.
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
    if (turn.phase !== "idle") {
      turnWasLiveRef.current = true;
    } else if (document.visibilityState === "visible") {
      turnWasLiveRef.current = false;
    }
  }, [turn.phase]);

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
  // view via SessionTerminalBridge on the same stream.
  const liveTurn = activeMode === "chat" ? turn : { phase: "idle" as const };
  // Never carry a stale open overlay across a switch back to chat and later
  // back to terminal — a bare `!showHistory` gate on `open` below would hide
  // it visually but leave the state true, popping it back open unprompted.
  if (terminalComposerOpen && activeMode !== "terminal") {
    setTerminalComposerOpen(false);
  }

  return (
    <div className={styles.tab}>
      <ConversationHeader
        session={session}
        shellOpen={shellOpen}
        onToggleShell={() => setShellOpen((x) => !x)}
        onRestartAgent={() => void routerClient.bounce(sessionId)}
        onUpdateSession={handleUpdateSession}
      />

      <div className={styles.body}>
        <div className={styles.stream}>
          <div className={styles.modeSwitch}>
            <Chip
              tone="neutral"
              pressed={activeMode === "chat"}
              onClick={() => void handleToggle("chat")}
            >
              Chat
            </Chip>
            <Chip
              tone="neutral"
              pressed={activeMode === "terminal"}
              onClick={() => void handleToggle("terminal")}
            >
              Terminal
            </Chip>
          </div>

          {showHistory ? (
            <>
              {provisioning && (
                <div className={styles.provisioningWrap}>
                  <ProvisioningStrip sessionId={sessionId} onDone={onProvisioningDone} />
                </div>
              )}
              <SessionSummaryCard session={session} />
              {messages.length === 0 && liveTurn.phase === "idle" ? (
                <div className={styles.emptyTranscript}>No conversation yet</div>
              ) : (
                <ChatRenderer
                  messages={messages}
                  links={links}
                  viewedSessionId={sessionId}
                  streamingText={liveTurn.phase === "streaming" ? liveTurn.text : undefined}
                  working={
                    liveTurn.phase === "waiting"
                      ? { tool: liveTurn.tool, blocked: liveTurn.blocked }
                      : false
                  }
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
              <Composer
                routerClient={routerClient}
                sessionId={sessionId}
                agentId={session.agent}
                provider={session.provider}
                disabled={transport.status !== "idle" || waking}
                initialValue={pendingFirstMessage}
                autoSendInitial={Boolean(pendingFirstMessage)}
                onSendStart={() => {
                  if (session.activity !== "active") setWaking(true);
                  setTurn((t) => (t.phase === "idle" ? { phase: "waiting" } : t));
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
            <ShellDock bridge={ptyBridge} cwd={session.worktree_dir ?? session.workspace} />
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
