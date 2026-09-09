import { Chip } from "../../components/Chip/Chip";
import { FLAGS } from "../../featureFlags";
import { foldToolResults } from "../../router/toolPairing";
import type { Link, ToolUse, TranscriptMessage } from "../../router/types";
import { Markdown } from "./markdown";
import { ToolCard } from "./ToolCard";
import { WorkingIndicator } from "./WorkingIndicator";
import styles from "./ChatRenderer.module.css";

/**
 * One item of an in-flight turn, built live from the stream: a run of streamed
 * text, or a tool card (its result attached as tool_result frames arrive).
 * Text tokens accumulate into the trailing text item so prose streams smoothly,
 * with no mid-turn transcript reconciliation to make it rubber-band.
 */
export type StreamItem =
  | { kind: "text"; text: string }
  | { kind: "tool"; tool: ToolUse };

export interface ChatRendererProps {
  messages: TranscriptMessage[];
  /** Linked sessions (flag-gated): links touching the viewed session, used to resolve an attributed message as an inject chip vs. a digest card (decision 4, docs/LINKED-SESSIONS-DESIGN.md). */
  links?: Link[];
  viewedSessionId?: string;
  /** The in-flight turn's ordered items (text runs + tool cards), rendered after `messages` and replaced by the persisted transcript on turn_end. */
  liveItems?: StreamItem[];
  /** Trailing "agent is working" affordance — shown for the entire live turn (send to turn_end), naming the pending tool when there is one. `blocked` overrides it with a system-toned banner. */
  working?: { tool?: ToolUse; blocked?: string } | false;
}

const CADENCE_LABEL: Record<Link["cadence"], string> = {
  per_turn: "cadence: digest after each burst of activity",
  manual_only: "cadence: manual — no automatic digests",
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
  liveItems = [],
  working,
}: ChatRendererProps) {
  const linksById = new Map(links.map((l) => [l.id, l]));
  const folded = foldToolResults(messages);
  return (
    <div className={styles.stream}>
      {folded.map((message, i) => {
        if (message.tool) {
          return <ToolCard key={i} tool={message.tool} timestamp={message.timestamp} />;
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
          <div
            key={i}
            className={styles[message.role] ?? styles.agent}
            // Anchor for the Ctrl+↑/↓ "hop between my messages" shortcut.
            data-user-msg={message.role === "user" ? "" : undefined}
          >
            <Markdown className={styles.prose} text={message.text ?? ""} />
          </div>
        );
      })}
      {liveItems.map((item, i) =>
        item.kind === "text" ? (
          <div key={`live-${i}`} className={styles.agent}>
            <Markdown className={styles.prose} text={item.text} />
          </div>
        ) : (
          <ToolCard key={`live-${i}`} tool={item.tool} live />
        ),
      )}
      {working && <WorkingIndicator tool={working.tool} blocked={working.blocked} />}
    </div>
  );
}
