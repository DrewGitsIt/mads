import { Chip } from "../../components/Chip/Chip";
import type { TranscriptMessage } from "../../router/types";
import { Markdown } from "./markdown";
import { ToolCard } from "./ToolCard";
import styles from "./ChatRenderer.module.css";

export interface ChatRendererProps {
  messages: TranscriptMessage[];
}

/**
 * Maps a transcript to prose + tool cards. Full-width stream: only the
 * prose wrapper is capped at the ~92ch reading measure — tool cards and
 * code blocks use the available room (MADS-UX §3.3).
 */
export function ChatRenderer({ messages }: ChatRendererProps) {
  return (
    <div className={styles.stream}>
      {messages.map((message, i) => {
        if (message.tool) {
          return <ToolCard key={i} tool={message.tool} />;
        }
        // Injected messages carry role "user" at the transport level, but
        // must never render as the subject user's own turn (decision 2,
        // docs/LINKED-SESSIONS-DESIGN.md) — attribution wins over role.
        if (message.attribution) {
          return (
            <div key={i} className={styles.injected}>
              <Chip tone="info">
                ⇄ from Leader · {message.attribution.source_name}
              </Chip>
              <Markdown className={styles.prose} text={message.text ?? ""} />
            </div>
          );
        }
        return (
          <div key={i} className={styles[message.role] ?? styles.agent}>
            <Markdown className={styles.prose} text={message.text ?? ""} />
          </div>
        );
      })}
    </div>
  );
}
