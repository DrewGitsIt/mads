import type { Usage } from "../../router/types";
import styles from "./ContextMeter.module.css";

export interface ContextMeterProps {
  /** The most recent API call's usage (turn_end stream frame, or session.last_usage at wake time). */
  usage?: Usage;
  /**
   * Fallback context window size in tokens (the static modelDisplay.ts
   * table), used only when usage.context_window is absent — the provider's
   * own runtime-reported window is authoritative when it exists (a local
   * 128k model would otherwise be judged against 200k).
   */
  contextWindow?: number;
}

/**
 * Compact usage gauge. Occupancy prefers usage.context_tokens — the prompt
 * side of the turn's FINAL API call, which is what the next request will
 * actually carry as context. The summed token fields are only a fallback for
 * providers that don't report per-call occupancy: on claude-code they are a
 * per-turn billing aggregate (cache reads summed across every tool
 * round-trip) that overstates occupancy by roughly the iteration count,
 * which used to peg this meter at 100% on tool-heavy turns. Undefined-safe:
 * renders nothing without usage and some window size.
 */
export function ContextMeter({ usage, contextWindow }: ContextMeterProps) {
  const window = usage?.context_window || contextWindow;
  if (usage === undefined || window === undefined || window <= 0) {
    return null;
  }

  const usedTokens =
    usage.context_tokens ||
    usage.input_tokens + (usage.cache_read_input_tokens ?? 0) + (usage.cache_creation_input_tokens ?? 0);
  const pct = Math.min(100, Math.round((usedTokens / window) * 100));
  const tone = pct >= 90 ? styles.bad : pct >= 70 ? styles.warn : styles.ok;

  return (
    <div className={styles.meter} title={`${usedTokens.toLocaleString()} / ${window.toLocaleString()} tokens`}>
      <div className={styles.track}>
        <div className={[styles.fill, tone].join(" ")} style={{ width: `${pct}%` }} />
      </div>
      <span className={styles.label}>{pct}%</span>
    </div>
  );
}
