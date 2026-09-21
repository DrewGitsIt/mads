import { Chip } from "../../components/Chip/Chip";
import { FLAGS } from "../../featureFlags";
import { foldToolResults } from "../../router/toolPairing";
import type { Link, ToolStatus, ToolUse, TranscriptMessage } from "../../router/types";
import { ActivityGroup } from "../activity/ActivityGroup";
import {
  buildItems,
  mergeTranscript,
  type SourceEntry,
  type TaskModel,
} from "../activity/conversationItems";
import { PlanWidget } from "../activity/PlanWidget";
import { TaskCard } from "../activity/TaskCard";
import { Markdown } from "./markdown";
import styles from "./ChatRenderer.module.css";

/**
 * One item of an in-flight turn, built live from the stream: a run of streamed
 * text, a tool call (its result attached as tool_result frames arrive), or a
 * whole thinking block. Text tokens accumulate into the trailing text item so
 * prose streams smoothly, with no mid-turn transcript reconciliation to make
 * it rubber-band.
 */
export type StreamItem =
  | { kind: "text"; text: string }
  | { kind: "tool"; tool: ToolUse; timestamp?: string }
  | { kind: "thinking"; text: string };

export interface ChatRendererProps {
  messages: TranscriptMessage[];
  /** Linked sessions (flag-gated): links touching the viewed session, used to resolve an attributed message as an inject chip vs. a digest card (decision 4, docs/LINKED-SESSIONS-DESIGN.md). */
  links?: Link[];
  viewedSessionId?: string;
  /** The in-flight turn's ordered items (text runs + tool/thinking entries), rendered after `messages` and replaced by the persisted transcript on turn_end. */
  liveItems?: StreamItem[];
  /** A turn is in flight — the trailing live activity group renders open and grows in place, collapsing on turn end. */
  turnActive?: boolean;
  /** Historical main-agent thinking blocks (session.thinking envelopes), merged into the stream by timestamp. */
  thinkingHistory?: { text: string; timestamp?: string }[];
  /** Out-of-band status changes (session.tool_status_changed + replay), by tool_use id. */
  toolStatuses?: ReadonlyMap<string, ToolStatus>;
  /** Task subagent lifecycles keyed by the spawning tool_use id — drives Task cards. */
  tasks?: ReadonlyMap<string, TaskModel>;
  /** Tool whose detail is open in the dock — highlights its row and suppresses its peek. */
  selectedToolId?: string;
  /** Row click → open/retarget the bottom dock on this tool. */
  onOpenTool?: (tool: ToolUse) => void;
  /** Task card click → open the subagent's stream in the dock (keyed by spawning tool_use id). */
  onOpenTask?: (toolUseId: string, tool: ToolUse) => void;
}

const CADENCE_LABEL: Record<Link["cadence"], string> = {
  per_turn: "cadence: digest after each burst of activity",
  on_signal: "cadence: wakes only when the session needs attention",
  manual_only: "cadence: manual — no automatic digests",
};

/**
 * Maps a transcript to prose + always-grouped activity (T12): consecutive
 * tool/thinking events render as one collapsible group of rows, TodoWrite
 * becomes the in-place-updating plan widget (T14), and Agent spawns become
 * Task subagent cards (T15). Live and historical rendering share the same
 * components — the only live-only difference is entrance motion and the
 * open-group treatment (`turnActive`).
 */
export function ChatRenderer({
  messages,
  links = [],
  viewedSessionId,
  liveItems = [],
  turnActive = false,
  thinkingHistory = [],
  toolStatuses,
  tasks,
  selectedToolId,
  onOpenTool,
  onOpenTask,
}: ChatRendererProps) {
  const linksById = new Map(links.map((l) => [l.id, l]));
  const folded = foldToolResults(messages);

  const entries: SourceEntry[] = mergeTranscript(folded, thinkingHistory);
  for (const item of liveItems) {
    if (item.kind === "text") {
      entries.push({ type: "message", message: { role: "agent", text: item.text }, live: true });
    } else if (item.kind === "tool") {
      entries.push({ type: "tool", tool: item.tool, timestamp: item.timestamp, live: true });
    } else {
      entries.push({ type: "thinking", text: item.text, live: true });
    }
  }
  const items = buildItems(entries);

  return (
    <div className={styles.stream}>
      {items.map((item) => {
        if (item.type === "activity") {
          return (
            <ActivityGroup
              key={item.key}
              entries={item.entries}
              live={Boolean(item.live && turnActive)}
              statuses={toolStatuses}
              selectedToolId={selectedToolId}
              onOpenTool={onOpenTool}
            />
          );
        }
        if (item.type === "plan") {
          return <PlanWidget key={item.key} todos={item.todos} />;
        }
        if (item.type === "task") {
          const model = item.tool.id ? tasks?.get(item.tool.id) : undefined;
          return (
            <TaskCard
              key={item.key}
              tool={item.tool}
              model={model}
              onOpen={
                onOpenTask && item.tool.id ? () => onOpenTask(item.tool.id!, item.tool) : undefined
              }
            />
          );
        }
        const message = item.message;
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
              <div key={item.key} className={styles.digest}>
                <div className={styles.digestHeader}>
                  ⌗ digest · <b>{message.attribution.source_name}</b>
                </div>
                <Markdown className={styles.prose} text={message.text ?? ""} />
                <div className={styles.digestCadence}>{CADENCE_LABEL[link.cadence]}</div>
              </div>
            );
          }
          return (
            <div key={item.key} className={styles.injected}>
              <Chip tone="info">
                ⇄ from Leader · {message.attribution.source_name}
              </Chip>
              <Markdown className={styles.prose} text={message.text ?? ""} />
            </div>
          );
        }
        return (
          <div
            key={item.key}
            className={styles[message.role] ?? styles.agent}
            // Anchor for the Ctrl+↑/↓ "hop between my messages" shortcut.
            data-user-msg={message.role === "user" ? "" : undefined}
          >
            <Markdown className={styles.prose} text={message.text ?? ""} />
          </div>
        );
      })}
    </div>
  );
}
