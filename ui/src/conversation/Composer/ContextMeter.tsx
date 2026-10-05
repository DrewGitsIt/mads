import { contextOccupancy } from "../../router/modelDisplay";
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
 * Compact usage gauge, beside the composer's Send. The occupancy arithmetic (and the reason it prefers
 * usage.context_tokens over the summed billing aggregate, which used to peg
 * this meter at 100% on tool-heavy turns) lives in contextOccupancy, shared
 * with the session rail's tooltip. Undefined-safe: renders nothing without
 * usage and some window size.
 */
export function ContextMeter({ usage, contextWindow }: ContextMeterProps) {
  const occupancy = contextOccupancy(usage, contextWindow);
  if (occupancy === undefined) {
    return null;
  }

  const { used, window, pct } = occupancy;
  const tone = pct >= 90 ? styles.bad : pct >= 70 ? styles.warn : styles.ok;

  return (
    <div
      className={styles.meter}
      data-testid="context-meter"
      title={`Context window: ${used.toLocaleString()} / ${window.toLocaleString()} tokens`}
    >
      <div className={styles.track}>
        <div className={[styles.fill, tone].join(" ")} style={{ width: `${pct}%` }} />
      </div>
      <span className={styles.label}>{pct}%</span>
    </div>
  );
}
