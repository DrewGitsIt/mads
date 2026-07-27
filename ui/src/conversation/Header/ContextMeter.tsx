import type { Usage } from "../../router/types";
import styles from "./ContextMeter.module.css";

export interface ContextMeterProps {
  /** The most recent API call's usage (turn_end stream frame, or session.last_usage at wake time). */
  usage?: Usage;
  /**
   * Context window size in tokens. No models-catalog endpoint exists yet to
   * look this up (HANDOFF.md §4 — data gap), so this is a plain prop rather
   * than an internal lookup; renders nothing until a caller has both
   * numbers.
   */
  contextWindow?: number;
}

/**
 * Compact usage gauge. Occupancy is the *prompt* side of the most recent API
 * call — input + cache tokens, excluding output_tokens — since that's what
 * the next turn's request will actually carry as context, not a running sum
 * of everything the conversation has produced. Undefined-safe: renders
 * nothing if either prop is missing.
 */
export function ContextMeter({ usage, contextWindow }: ContextMeterProps) {
  if (usage === undefined || contextWindow === undefined || contextWindow <= 0) {
    return null;
  }

  const usedTokens =
    usage.input_tokens + (usage.cache_read_input_tokens ?? 0) + (usage.cache_creation_input_tokens ?? 0);
  const pct = Math.min(100, Math.round((usedTokens / contextWindow) * 100));
  const tone = pct >= 90 ? styles.bad : pct >= 70 ? styles.warn : styles.ok;

  return (
    <div className={styles.meter} title={`${usedTokens.toLocaleString()} / ${contextWindow.toLocaleString()} tokens`}>
      <div className={styles.track}>
        <div className={[styles.fill, tone].join(" ")} style={{ width: `${pct}%` }} />
      </div>
      <span className={styles.label}>{pct}%</span>
    </div>
  );
}
