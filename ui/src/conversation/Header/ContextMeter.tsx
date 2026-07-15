import styles from "./ContextMeter.module.css";

export interface ContextMeterProps {
  /** Tokens used so far this session (from the last turn's Usage). */
  usedTokens?: number;
  /**
   * Context window size in tokens. No models-catalog endpoint exists yet to
   * look this up (HANDOFF.md §4 — data gap), so this is a plain prop rather
   * than an internal lookup; renders nothing until a caller has both
   * numbers.
   */
  contextWindow?: number;
}

/** Compact usage gauge. Undefined-safe: renders nothing if either prop is missing. */
export function ContextMeter({ usedTokens, contextWindow }: ContextMeterProps) {
  if (usedTokens === undefined || contextWindow === undefined || contextWindow <= 0) {
    return null;
  }

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
