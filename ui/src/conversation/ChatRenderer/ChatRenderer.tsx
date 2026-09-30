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
import { CompactionMarker } from "./CompactionMarker";
import { formatMessageTime, parseMessageTime } from "./messageTime";
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

/** A digest push the monitor answered with exactly NOOP (the framing every
 * monitor-facing push carries) collapses to a quiet-check chip; consecutive
 * quiet checks aggregate into one. */
type QuietItem = { key: string; type: "quiet"; count: number; sources: string[] };

/** Would this message render as a digest card (vs. an inject chip)? The
 * viewed session being the attributed link's granting side is what makes it
 * the monitor's copy (decision 4, docs/LINKED-SESSIONS-DESIGN.md). */
function isDigestCard(
  message: TranscriptMessage,
  linksById: Map<string, Link>,
  viewedSessionId?: string,
): boolean {
  if (!message.attribution || !FLAGS.linkedSessions) return false;
  const link = linksById.get(message.attribution.link_id);
  return Boolean(link && viewedSessionId === link.granting_session_id);
}

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

  // Collapse digest→NOOP pairs into quiet-check chips before rendering. A
  // completed (non-live) agent reply of exactly NOOP directly after a digest
  // card is the monitor saying "nothing to do" — showing both in full would
  // pay the transcript cost the noop protocol exists to avoid.
  const rendered: (ReturnType<typeof buildItems>[number] | QuietItem)[] = [];
  for (let i = 0; i < items.length; i++) {
    const item = items[i];
    const next = items[i + 1];
    if (
      item.type === "message" &&
      isDigestCard(item.message, linksById, viewedSessionId) &&
      next?.type === "message" &&
      !next.live &&
      next.message.role === "agent" &&
      (next.message.text ?? "").trim() === "NOOP"
    ) {
      const source = item.message.attribution!.source_name;
      const last = rendered[rendered.length - 1];
      if (last?.type === "quiet") {
        last.count++;
        if (!last.sources.includes(source)) last.sources.push(source);
      } else {
        rendered.push({ key: item.key, type: "quiet", count: 1, sources: [source] });
      }
      i++; // the NOOP reply is consumed by the chip
      continue;
    }
    rendered.push(item);
  }

  return (
    <div className={styles.stream}>
      {rendered.map((item) => {
        if (item.type === "quiet") {
          return (
            <div key={item.key} className={styles.quiet}>
              <Chip tone="neutral">
                {item.count === 1 ? "1 quiet check" : `${item.count} quiet checks`} ·{" "}
                {item.sources.join(", ")} · nothing needed
              </Chip>
            </div>
          );
        }
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
        if (message.role === "compact") {
          return (
            <CompactionMarker
              key={item.key}
              marker={message.compact ?? {}}
              summary={message.text}
              timestamp={message.timestamp}
            />
          );
        }
        if (message.role === "notification") {
          // The harness telling the agent a background task finished, which
          // can start a turn nobody typed. Drawn as a quiet system line, not
          // in the human's voice: as a user bubble it was a wall of raw
          // <task-notification> XML the human never wrote.
          const at = parseMessageTime(message.timestamp);
          return (
            <div key={item.key} className={styles.notification} role="note">
              <span aria-hidden="true">⏺</span>
              <span className={styles.notificationText} title={message.text}>
                {message.text || "Background task finished"}
              </span>
              {at && (
                <time className={styles.notificationTime} dateTime={at.toISOString()} title={at.toLocaleString()}>
                  {formatMessageTime(at)}
                </time>
              )}
            </div>
          );
        }
        const at = parseMessageTime(message.timestamp);
        const stamp = at && (
          <time className={styles.stamp} dateTime={at.toISOString()} title={at.toLocaleString()}>
            {formatMessageTime(at)}
          </time>
        );
        const isUser = message.role === "user";
        return (
          <div
            key={item.key}
            className={styles[message.role] ?? styles.agent}
            // Anchor for the Ctrl+↑/↓ "hop between my messages" shortcut.
            data-user-msg={isUser ? "" : undefined}
          >
            {/* Revealed on hover (styles.stamp), on the bubble's open side. */}
            {isUser && stamp}
            <Markdown className={styles.prose} text={message.text ?? ""} />
            {!isUser && stamp}
          </div>
        );
      })}
    </div>
  );
}
