import { useEffect, useState } from "react";
import { Chip } from "../../components/Chip/Chip";
import type { ToolUse } from "../../router/types";
import { toolTitle } from "../activity/toolDisplay";
import styles from "./WorkingIndicator.module.css";

/**
 * Turn-long "agent is working" affordance, V2 (mockup's .wi): mounted for
 * the entire live turn (ConversationTab keeps `working` truthy from send to
 * turn_end), so "is it still going?" is never ambiguous — the elapsed
 * counter ticks turn-long, and a current-activity ticker line streams the
 * latest tool title (or "Thinking", fed by thinking-token ticks) while the
 * label stays a calm, generic "Working…". Precedence is unchanged:
 * `blocked` overrides everything with a system-toned banner; a pending tool
 * names itself (aria) ahead of the generic label — see providers/
 * claudecode's README ("Defense in depth") for why blocked must never be a
 * silent no-op.
 */
export function WorkingIndicator({
  tool,
  blocked,
  activity,
}: {
  tool?: ToolUse;
  blocked?: string;
  /** Streamed current-activity ticker — latest tool title or "Thinking…". */
  activity?: string;
}) {
  // Seconds since this indicator mounted — i.e. since the turn went live,
  // because the component stays mounted turn-long. Deliberately not reset
  // when the pending tool changes: the user's question is "how long has the
  // agent been at it", not per-tool timing (the rows carry that).
  const [elapsed, setElapsed] = useState(0);
  useEffect(() => {
    const started = Date.now();
    const id = window.setInterval(
      () => setElapsed(Math.floor((Date.now() - started) / 1000)),
      1000,
    );
    return () => window.clearInterval(id);
  }, []);

  if (blocked) {
    return (
      <div className={styles.working} role="status">
        <Chip tone="bad">{blocked}</Chip>
      </div>
    );
  }
  const label = tool ? `Using ${tool.name}…` : "Working…";
  const ticker = activity ?? (tool ? toolTitle(tool) : undefined);
  return (
    <div className={styles.working} role="status" aria-label={label}>
      <span className={styles.dot} aria-hidden="true" />
      <span className={styles.dot} aria-hidden="true" />
      <span className={styles.dot} aria-hidden="true" />
      <span className={styles.label}>Working…</span>
      {ticker && (
        <span className={styles.ticker} data-testid="working-ticker" key={ticker}>
          {ticker}
        </span>
      )}
      {elapsed > 0 && <span className={styles.elapsed}>{elapsed}s</span>}
    </div>
  );
}
