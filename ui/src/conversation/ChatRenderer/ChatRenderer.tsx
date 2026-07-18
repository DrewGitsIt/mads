import { Chip } from "../../components/Chip/Chip";
import { FLAGS } from "../../featureFlags";
import type { Link, ToolUse, TranscriptMessage } from "../../router/types";
import { Markdown } from "./markdown";
import { ToolCard } from "./ToolCard";
import { WorkingIndicator } from "./WorkingIndicator";
import styles from "./ChatRenderer.module.css";

export interface ChatRendererProps {
  messages: TranscriptMessage[];
  /** Linked sessions (flag-gated): links touching the viewed session, used to resolve an attributed message as an inject chip vs. a digest card (decision 4, docs/LINKED-SESSIONS-DESIGN.md). */
  links?: Link[];
  viewedSessionId?: string;
  /** In-progress assistant text for the turn currently streaming — appended after `messages`, replaced by the persisted transcript on turn_end. */
  streamingText?: string;
  /** Pre-first-chunk "agent is working" state; carries the most recent tool_use so the indicator can name it. Ignored once streamingText is set. */
  working?: { tool?: ToolUse } | false;
}

const CADENCE_LABEL: Record<Link["cadence"], string> = {
  per_turn: "cadence: per turn",
  manual_only: "cadence: manual only",
};

/**
 * Maps a transcript to prose + tool cards. Full-width stream: only the
 * prose wrapper is capped at the ~92ch reading measure — tool cards and
 * code blocks use the available room (MADS-UX §3.3).
 */
export function ChatRenderer({
  messages,
  links = [],
  viewedSessionId,
  streamingText,
  working,
}: ChatRendererProps) {
  const linksById = new Map(links.map((l) => [l.id, l]));
  return (
    <div className={styles.stream}>
      {messages.map((message, i) => {
        if (message.tool) {
          return <ToolCard key={i} tool={message.tool} />;
        }
        // Injected messages carry role "user" at the transport level, but
        // must never render as the subject user's own turn (decision 2,
        // docs/LINKED-SESSIONS-DESIGN.md) — attribution wins over role.
        // The wire's Attribution shape is identical for a leader's inject
        // and a monitor's digest; resolve which one this is by comparing
        // the attributed link against the session being viewed (decision 4).
        if (message.attribution && FLAGS.linkedSessions) {
          const link = linksById.get(message.attribution.link_id);
          if (link && viewedSessionId === link.granting_session_id) {
            return (
              <div key={i} className={styles.digest}>
                <div className={styles.digestHeader}>
                  ⌗ digest · <b>{message.attribution.source_name}</b>
                </div>
                <Markdown className={styles.prose} text={message.text ?? ""} />
                <div className={styles.digestCadence}>{CADENCE_LABEL[link.cadence]}</div>
              </div>
            );
          }
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
      {streamingText !== undefined && (
        <div className={styles.agent}>
          <Markdown className={styles.prose} text={streamingText} />
        </div>
      )}
      {!streamingText && working && <WorkingIndicator tool={working.tool} />}
    </div>
  );
}
