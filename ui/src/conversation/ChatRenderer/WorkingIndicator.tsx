import { useEffect, useRef, useState } from "react";
import type { ToolUse } from "../../router/types";
import { toolTitle } from "../activity/toolDisplay";
import styles from "./WorkingIndicator.module.css";

const WORKING = "Working…";
const COMPACTING = "Compacting…";

/**
 * Turn-long "agent is working" affordance, V2 (mockup's .wi): mounted for
 * the entire live turn (ConversationTab keeps `working` truthy from send to
 * turn_end), so "is it still going?" is never ambiguous — the elapsed
 * counter ticks turn-long, and a current-activity ticker line streams the
 * latest tool title (or "Thinking", fed by thinking-token ticks) while the
 * label stays a calm, generic "Working…". A pending tool names itself
 * (aria) ahead of the generic label. A turn parked on a permission prompt
 * isn't this indicator's story: the conversation's approval jump owns that
 * signal, live turn or not (a background subagent can ask after the turn).
 */
export function WorkingIndicator({
  tool,
  activity,
  since,
}: {
  tool?: ToolUse;
  /** Streamed current-activity ticker — latest tool title or "Thinking…". */
  activity?: string;
  /**
   * The turn's server-side start (Session.agent_state.since). Anchors the
   * counter to the real turn, so reopening a conversation mid-turn doesn't
   * restart it at 0s; mount time stands in until it's known.
   */
  since?: string;
}) {
  // Seconds since the turn went live — since `since` when known, else since
  // this indicator mounted (it stays mounted turn-long). Deliberately not
  // reset when the pending tool changes: the user's question is "how long
  // has the agent been at it", not per-tool timing (the rows carry that).
  const mountedAt = useRef(Date.now());
  const [elapsed, setElapsed] = useState(0);
  useEffect(() => {
    const parsed = since ? Date.parse(since) : NaN;
    const started = Number.isNaN(parsed) ? mountedAt.current : Math.min(parsed, Date.now());
    const tick = () => setElapsed(Math.floor((Date.now() - started) / 1000));
    tick();
    const id = window.setInterval(tick, 1000);
    return () => window.clearInterval(id);
  }, [since]);

  // The session's mirrored labels (services/session's agentstate.go) include
  // two that describe the whole turn rather than a step in it: the generic
  // "Working…", which would only echo the label beside it, and
  // "Compacting…", which replaces that label — a compaction turn has no
  // steps to tick through.
  const compacting = activity === COMPACTING;
  const ticker =
    activity === WORKING || compacting ? undefined : (activity ?? (tool ? toolTitle(tool) : undefined));
  const shown = compacting ? COMPACTING : WORKING;
  const label = tool ? `Using ${tool.name}…` : shown;
  return (
    <div className={styles.working} role="status" aria-label={label}>
      <span className={styles.dot} aria-hidden="true" />
      <span className={styles.dot} aria-hidden="true" />
      <span className={styles.dot} aria-hidden="true" />
      <span className={styles.label}>{shown}</span>
      {ticker && (
        <span className={styles.ticker} data-testid="working-ticker" key={ticker}>
          {ticker}
        </span>
      )}
      {elapsed > 0 && <span className={styles.elapsed}>{elapsed}s</span>}
    </div>
  );
}
