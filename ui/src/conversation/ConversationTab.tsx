import { useEffect, useState } from "react";
import { Chip } from "../components/Chip/Chip";
import type { RouterClient } from "../router/client";
import type { Session, TranscriptMessage } from "../router/types";
import type { PtyBridge } from "./TerminalRenderer/ptyBridge";
import { ConversationHeader } from "./Header/ConversationHeader";
import { ChatRenderer } from "./ChatRenderer/ChatRenderer";
import { TerminalView } from "./TerminalRenderer/TerminalView";
import { SessionSummaryCard } from "./SummaryCard/SessionSummaryCard";
import { Composer } from "./Composer/Composer";
import { ShellDock } from "./ShellDock/ShellDock";
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
  const [mode, setMode] = useState<DisplayMode>("chat");
  const [shellOpen, setShellOpen] = useState(false);

  useEffect(() => {
    let cancelled = false;
    void routerClient.getSession(sessionId).then((s) => {
      if (!cancelled) setSession(s);
    });
    void routerClient.getTranscript(sessionId).then((m) => {
      if (!cancelled) setMessages(m);
    });
    return () => {
      cancelled = true;
    };
  }, [routerClient, sessionId]);

  if (!session) return null;

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
              <ChatRenderer messages={messages} />
              <Composer
                routerClient={routerClient}
                sessionId={sessionId}
                agentId={session.agent}
                onSent={(text) =>
                  setMessages((m) => [...m, { role: "user", text }])
                }
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
