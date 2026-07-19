import { useEffect, useMemo, useRef, useState } from "react";
import type { Task, TranscriptMessage } from "../../router/types";
import type { StageAttempt } from "../state/usePipelineRun";
import { elapsedSeconds, formatClock, pendingTool, toolArgument, truncateMiddle } from "./feedFormat";
import styles from "./AgentFeedView.module.css";

export interface AgentFeedViewProps {
  task: Task;
  attempts: StageAttempt[];
  turns: TranscriptMessage[];
}

// How close to the bottom (px) still counts as "following" — the
// scroll-lock-on-scrollback threshold (DESIGN point 4).
const FOLLOW_THRESHOLD_PX = 24;

/**
 * Transparency-first drill-in feed: normalized provider turns rendered as
 * prose rows (agent/user) visually distinct from compact tool rows (tool
 * name + salient Bash command/file path), each with a visible timestamp, a
 * ticking liveness indicator on the in-flight tool call, and auto-follow
 * that disengages once the viewer scrolls back through history.
 */
export function AgentFeedView({ task, attempts, turns }: AgentFeedViewProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const followRef = useRef(true);
  const pending = useMemo(() => pendingTool(turns), [turns]);

  useEffect(() => {
    const el = containerRef.current;
    if (!el || !followRef.current) return;
    el.scrollTop = el.scrollHeight;
  }, [turns]);

  function handleScroll() {
    const el = containerRef.current;
    if (!el) return;
    followRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < FOLLOW_THRESHOLD_PX;
  }

  const currentAttempt = attempts[attempts.length - 1];
  const started = turns[0]?.timestamp;

  return (
    <div className={styles.feed} data-testid="agent-feed">
      <div className={styles.meta} data-testid="feed-header">
        <span className={styles.metaTask}>{task.title}</span>
        {currentAttempt && (
          <span className={styles.metaStage}>
            {currentAttempt.stage} · attempt {currentAttempt.attempt}
          </span>
        )}
        {started && <span className={styles.metaStarted}>started {formatClock(started)}</span>}
      </div>
      <div className={styles.rows} ref={containerRef} onScroll={handleScroll} data-testid="feed-rows">
        {turns.map((turn, i) => (
          <FeedRow key={i} turn={turn} pending={turn === pending} />
        ))}
      </div>
    </div>
  );
}

function FeedRow({ turn, pending }: { turn: TranscriptMessage; pending: boolean }) {
  const time = turn.timestamp ? formatClock(turn.timestamp) : undefined;

  if (turn.role === "tool" || turn.role === "tool_result") {
    return <ToolRow turn={turn} pending={pending} time={time} />;
  }

  return (
    <div className={styles.prose} data-role={turn.role} data-testid="feed-prose-row">
      {time && <span className={styles.time}>{time}</span>}
      <span className={styles.speaker}>{turn.role === "user" ? "user" : "agent"}</span>
      <span className={styles.text}>{turn.text}</span>
    </div>
  );
}

function ToolRow({
  turn,
  pending,
  time,
}: {
  turn: TranscriptMessage;
  pending: boolean;
  time?: string;
}) {
  if (turn.role === "tool_result") {
    return (
      <div className={styles.toolResult} data-testid="feed-tool-result-row">
        {time && <span className={styles.time}>{time}</span>}
        <span className={styles.resultMark}>✓</span>
        {turn.text && <Truncatable value={turn.text} />}
      </div>
    );
  }

  const arg = turn.tool ? toolArgument(turn.tool) : "";
  return (
    <div className={styles.tool} data-testid="feed-tool-row" data-pending={pending || undefined}>
      {time && <span className={styles.time}>{time}</span>}
      <span className={styles.toolMark}>⏺</span>
      <span className={styles.toolName}>{turn.tool?.name}</span>
      {arg && <Truncatable value={arg} />}
      {pending && <Liveness since={turn.timestamp} />}
    </div>
  );
}

/** Middle-truncated by default; click toggles the full value, same idiom
 * as a long path in a diff view — no separate modal/tooltip machinery. */
function Truncatable({ value, max = 72 }: { value: string; max?: number }) {
  const [expanded, setExpanded] = useState(false);
  if (value.length <= max) {
    return <span className={styles.arg}>{value}</span>;
  }
  return (
    <button
      type="button"
      className={styles.arg}
      onClick={() => setExpanded((e) => !e)}
      title={value}
      data-testid="feed-truncatable"
    >
      {expanded ? value : truncateMiddle(value, max)}
    </button>
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
