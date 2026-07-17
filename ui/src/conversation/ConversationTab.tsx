import { useCallback, useEffect, useRef, useState } from "react";
import { Button } from "../components/Button/Button";
import { Card } from "../components/Card/Card";
import { Chip } from "../components/Chip/Chip";
import type { RouterClient } from "../router/client";
import { subscribeToEvents } from "../router/events";
import { SESSION_EVENT_TYPES } from "../router/sessionEventTypes";
import type { Session, TranscriptMessage } from "../router/types";
import type { PtyBridge } from "./TerminalRenderer/ptyBridge";
import { ConversationHeader } from "./Header/ConversationHeader";
import { ChatRenderer } from "./ChatRenderer/ChatRenderer";
import { TerminalView } from "./TerminalRenderer/TerminalView";
import { SessionSummaryCard } from "./SummaryCard/SessionSummaryCard";
import { Composer } from "./Composer/Composer";
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
}

type DisplayMode = "chat" | "terminal";

/**
 * Top-level Conversation tab (MADS-UX §3.3 / W6.3). Composes the header,
 * the chat/terminal display-mode switch, the inactive-session summary
 * card, the composer, and the docked Shell — see HANDOFF.md §8 for the
 * demo-mount rationale (no Session Workspace shell exists yet to register
 * this into).
 */
export function ConversationTab({ sessionId, routerClient, ptyBridge }: ConversationTabProps) {
  const [session, setSession] = useState<Session | null>(null);
  const [messages, setMessages] = useState<TranscriptMessage[]>([]);
  const [loadError, setLoadError] = useState(false);
  const [retryToken, setRetryToken] = useState(0);
  const [mode, setMode] = useState<DisplayMode>("chat");
  const [shellOpen, setShellOpen] = useState(false);

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

  useEffect(() => {
    return subscribeToEvents({ sessionId, types: SESSION_EVENT_TYPES }, refetchSession);
  }, [refetchSession, sessionId]);

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
            <Chip tone="neutral" pressed={mode === "chat"} onClick={() => setMode("chat")}>
              Chat
            </Chip>
            <Chip
              tone="neutral"
              pressed={mode === "terminal"}
              onClick={() => setMode("terminal")}
            >
              Terminal
            </Chip>
          </div>

          {mode === "chat" ? (
            <>
              <SessionSummaryCard session={session} />
              {messages.length === 0 ? (
                <div className={styles.emptyTranscript}>No conversation yet</div>
              ) : (
                <ChatRenderer messages={messages} />
              )}
              <Composer
                routerClient={routerClient}
                sessionId={sessionId}
                agentId={session.agent}
                onSent={(text) => {
                  setMessages((m) => [...m, { role: "user", text }]);
                  void pollForReply();
                }}
              />
            </>
          ) : ptyBridge ? (
            <TerminalView bridge={ptyBridge} className={styles.terminal} />
          ) : (
            <div className={styles.ptyUnavailable}>
              Terminal mode requires the Electron shell.
            </div>
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
    </div>
  );
}
