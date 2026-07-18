import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button } from "../components/Button/Button";
import { Card } from "../components/Card/Card";
import { Chip } from "../components/Chip/Chip";
import { Toast } from "../components/Toast/Toast";
import type { RouterClient, SessionEventSource } from "../router/client";
import { TransportSwitchError } from "../router/client";
import { subscribeToEvents } from "../router/events";
import { WORKSPACE_SESSION_EVENT_TYPES } from "../router/sessionEventTypes";
import type { Session, TranscriptMessage } from "../router/types";
import { useSessionLinks } from "../features/sessions/state/useSessionLinks";
import type { PtyBridge } from "./TerminalRenderer/ptyBridge";
import { SessionTerminalBridge } from "./TerminalRenderer/sessionTerminalBridge";
import { ConversationHeader } from "./Header/ConversationHeader";
import { ChatRenderer } from "./ChatRenderer/ChatRenderer";
import { TerminalView } from "./TerminalRenderer/TerminalView";
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
  const [eventSource, setEventSource] = useState<SessionEventSource | null>(null);
  const [sendError, setSendError] = useState<string>();
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
    return () => {
      cancelled = true;
    };
  }, [routerClient, sessionId, retryToken]);

  const refetchSession = useCallback(() => {
    void routerClient.getSession(sessionId).then(setSession);
  }, [routerClient, sessionId]);

  // Shares SessionWorkspace's session-scoped subscription (see
  // WORKSPACE_SESSION_EVENT_TYPES) rather than opening its own — GET
  // /transcript has no push notification of its own (see pollForReply
  // below), and a linked-session inject/digest lands on this session's
  // transcript out of band from anything this viewer sent, so it needs a
  // server-scoped trigger to refetch rather than waiting on a poll that
  // never starts.
  useEffect(() => {
    return subscribeToEvents(
      { sessionId, types: WORKSPACE_SESSION_EVENT_TYPES },
      (envelope) => {
        if (envelope.type === "link.message_injected") {
          void routerClient.getTranscript(sessionId).then(setMessages);
        } else {
          refetchSession();
        }
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
    return () => {
      source.close();
      setEventSource(null);
    };
  }, [routerClient, sessionId]);

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

  // GET /transcript has no push notification for turn completion (no domain
  // event fires per-turn, only on session start/shutdown), so a reply only
  // becomes visible by re-fetching. Bounded poll, not a general subscription:
  // gives up quietly after ~3s rather than polling indefinitely.
  const pollForReply = useCallback(async () => {
    const baseline = messagesRef.current.length;
    for (let attempt = 0; attempt < 10; attempt++) {
      await new Promise((resolve) => setTimeout(resolve, 300));
      try {
        const fresh = await routerClient.getTranscript(sessionId);
        if (fresh.length > baseline) {
          setMessages(fresh);
          return;
        }
      } catch {
        // transient — keep polling, last attempt just gives up quietly
      }
    }
  }, [routerClient, sessionId]);

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

  return (
    <div className={styles.tab}>
      <ConversationHeader
        session={session}
        shellOpen={shellOpen}
        onToggleShell={() => setShellOpen((x) => !x)}
        onRestartAgent={() => void routerClient.bounce(sessionId)}
        onUpdateSession={(patch) => setSession((s) => (s ? { ...s, ...patch } : s))}
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
              {messages.length === 0 ? (
                <div className={styles.emptyTranscript}>No conversation yet</div>
              ) : (
                <ChatRenderer messages={messages} links={links} viewedSessionId={sessionId} />
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
              <Composer
                routerClient={routerClient}
                sessionId={sessionId}
                agentId={session.agent}
                disabled={transport.status !== "idle"}
                initialValue={pendingFirstMessage}
                autoSendInitial={Boolean(pendingFirstMessage)}
                onSendError={setSendError}
                onAutoSendStart={onPendingFirstMessageHandled}
                onSent={(text) => {
                  setMessages((m) => [...m, { role: "user", text }]);
                  void pollForReply();
                }}
              />
            </>
          ) : terminalBridge ? (
            <TerminalView bridge={terminalBridge} className={styles.terminal} />
          ) : (
            <div className={styles.ptyUnavailable}>Connecting…</div>
          )}
        </div>

        {shellOpen &&
          (ptyBridge ? (
            <ShellDock bridge={ptyBridge} />
          ) : (
            <div className={styles.ptyUnavailable}>
              Shell requires the Electron shell.
            </div>
          ))}
      </div>

      {sendError && (
        <Toast
          tone="bad"
          title="Couldn't send your message"
          description={sendError}
          onDismiss={() => setSendError(undefined)}
        />
      )}
    </div>
  );
}
