import { contextOccupancy } from "../../router/modelDisplay";
import type { Usage } from "../../router/types";
import { formatTokens } from "./dockLabels";
import styles from "./ContextRim.module.css";

export interface ContextRimProps {
  /** The most recent API call's usage (turn_end frame, or session.last_usage at mount). Absent → plain hairline. */
  usage?: Usage;
  /** Fallback window (modelDisplay's static table); usage.context_window wins when present. */
  contextWindow?: number;
  /** Chat only: offers "Compact" in the label from 80%. Terminal users type /compact in their TUI. */
  onCompact?: () => void;
}

/** Tone thresholds, the same as the old composer meter's. */
const WARN_AT = 70;
const BAD_AT = 90;
const COMPACT_AT = 80;

/**
 * The dock's 2px top hairline, which is the context meter (mockup E): its
 * fill is occupancy, text3 below 70%, warn from 70%, bad from 90%. Hovering
 * the rim (a 14px hit band around the 2px line) shows
 * `106.5k / 128k · 83% context`; the label stays pinned from 70%, and from
 * 80% it carries a Compact link when `onCompact` is wired. Without usage
 * the rim is a plain hairline with no label — never a stale or made-up
 * number. The occupancy arithmetic is contextOccupancy's, shared with the
 * rail so the two can't disagree.
 */
export function ContextRim({ usage, contextWindow, onCompact }: ContextRimProps) {
  const occupancy = contextOccupancy(usage, contextWindow);
  if (!occupancy) {
    return <div className={styles.rim} data-rim="empty" />;
  }
  const { used, window, pct } = occupancy;
  const tone = pct >= BAD_AT ? "bad" : pct >= WARN_AT ? "warn" : "quiet";
  const pinned = pct >= WARN_AT;
  const tokens = `${formatTokens(used)} / ${formatTokens(window)}`;
  return (
    <div
      className={styles.rim}
      data-testid="context-meter"
      data-tone={tone}
      data-pinned={pinned || undefined}
      role="meter"
      aria-label="Context"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={pct}
      aria-valuetext={`${tokens} tokens, ${pct}% of the context window`}
      title={`Context window: ${used.toLocaleString()} / ${window.toLocaleString()} tokens`}
    >
      <i className={styles.fill} style={{ width: `${pct}%` }} />
      <span className={styles.label} data-testid="context-label">
        <span>
          <span className={styles.lblFull}>{tokens} · </span>
          {pct}%<span className={styles.lblFull}> context</span>
        </span>
        {onCompact && pct >= COMPACT_AT && (
          <button type="button" className={styles.compact} onClick={onCompact}>
            Compact
          </button>
        )}
      </span>
    </div>
  );
}
