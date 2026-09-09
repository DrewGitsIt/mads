import { useEffect, useState } from "react";
import { Chip } from "../../components/Chip/Chip";
import type { ToolUse } from "../../router/types";
import styles from "./WorkingIndicator.module.css";

/**
 * Turn-long "agent is working" affordance — mounted for the entire live turn
 * (ConversationTab keeps `working` truthy from send to turn_end), so "is it
 * still going?" is never ambiguous: the elapsed counter ticks as long as the
 * turn is live, and the label names the pending tool when there is one.
 * `blocked` overrides the pulsing-dots animation with a system-toned banner
 * when the provider reports a tool call stalled awaiting permission rather
 * than completing — see providers/claudecode's README ("Defense in depth")
 * for why this must never be a silent no-op.
 */
export function WorkingIndicator({ tool, blocked }: { tool?: ToolUse; blocked?: string }) {
  // Seconds since this indicator mounted — i.e. since the turn went live,
  // because the component stays mounted turn-long. Deliberately not reset
  // when the pending tool changes: the user's question is "how long has the
  // agent been at it", not per-tool timing (the cards carry that).
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
  return (
    <div className={styles.working} role="status" aria-label={label}>
      <span className={styles.dot} aria-hidden="true" />
      <span className={styles.dot} aria-hidden="true" />
      <span className={styles.dot} aria-hidden="true" />
      <span className={styles.label}>{label}</span>
      {elapsed > 0 && <span className={styles.elapsed}>{elapsed}s</span>}
    </div>
  );
}
