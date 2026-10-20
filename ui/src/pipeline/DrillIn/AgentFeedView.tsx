import { useEffect, useMemo, useState } from "react";
import { ToolCard } from "../../conversation/ChatRenderer/ToolCard";
import { useStickToBottom } from "../../conversation/useStickToBottom";
import type { Task, TranscriptMessage } from "../../router/types";
import type { StageAttempt } from "../state/usePipelineRun";
import { buildFeedRows, elapsedSeconds, formatClock, type FeedRow as FeedRowData } from "./feedFormat";
import styles from "./AgentFeedView.module.css";

export interface AgentFeedViewProps {
  task: Task;
  attempts: StageAttempt[];
  turns: TranscriptMessage[];
  /** The task/stage/started line. Off when the host already shows which attempt this is. */
  showHeader?: boolean;
}

/**
 * Transparency-first drill-in feed: normalized provider turns rendered as
 * prose rows (agent/user) visually distinct from compact tool rows (tool
 * name + salient Bash command/file path), each with a visible timestamp, a
 * ticking liveness indicator on the in-flight tool call, and auto-follow
 * that disengages once the viewer scrolls back through history.
 */
export function AgentFeedView({ task, attempts, turns, showHeader = true }: AgentFeedViewProps) {
  // Auto-follow that lets go on scrollback (DESIGN point 4).
  const stick = useStickToBottom();
  const rows = useMemo(() => buildFeedRows(turns), [turns]);

  const currentAttempt = attempts[attempts.length - 1];
  const started = turns[0]?.timestamp;

  return (
    <div className={styles.feed} data-testid="agent-feed">
      {showHeader && (
        <div className={styles.meta} data-testid="feed-header">
          <span className={styles.metaTask}>{task.title}</span>
          {currentAttempt && (
            <span className={styles.metaStage}>
              {currentAttempt.stage} · attempt {currentAttempt.attempt}
            </span>
          )}
          {started && <span className={styles.metaStarted}>started {formatClock(started)}</span>}
        </div>
      )}
      <div className={styles.rows} ref={stick.ref} onScroll={stick.onScroll} data-testid="feed-rows">
        {rows.map((row, i) => (
          <FeedRow key={i} row={row} />
        ))}
      </div>
    </div>
  );
}

function FeedRow({ row }: { row: FeedRowData }) {
  const time = row.timestamp ? formatClock(row.timestamp) : undefined;

  if (row.kind === "tool") {
    return (
      <div className={styles.toolRow} data-testid="feed-tool-row" data-pending={row.pending || undefined}>
        {time && <span className={styles.time}>{time}</span>}
        <div className={styles.toolCardWrap}>
          <ToolCard tool={row.tool} timestamp={row.timestamp} />
        </div>
        {row.pending && <Liveness since={row.timestamp} />}
      </div>
    );
  }

  return (
    <div className={styles.prose} data-role={row.role} data-testid="feed-prose-row">
      {time && <span className={styles.time}>{time}</span>}
      <span className={styles.speaker}>{row.role}</span>
      <span className={styles.text}>{row.text}</span>
    </div>
  );
}

/** Ticks once a second purely for display — the transcript itself never
 * gains a new entry while a tool call is in flight (DESIGN point 5: this is
 * presentation-only, it doesn't drive the data model). */
function Liveness({ since }: { since?: string }) {
  const [, tick] = useState(0);
  useEffect(() => {
    const id = setInterval(() => tick((n) => n + 1), 1000);
    return () => clearInterval(id);
  }, []);
  return (
    <span className={styles.liveness} data-testid="feed-liveness">
      running {since ? `${elapsedSeconds(since)}s` : "…"}
    </span>
  );
}
