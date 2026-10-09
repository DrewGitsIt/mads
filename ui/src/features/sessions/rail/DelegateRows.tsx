import { useState } from "react";
import type { Delegate, DelegateFade } from "../../../router/types";
import { KindIcon, Spinner, StatusGlyph } from "../../../conversation/activity/primitives";
import { formatElapsed, formatMs } from "../../../conversation/activity/toolDisplay";
import { useElapsedNow, useRelativeNow } from "../../../utils/relativeTime";
import styles from "./DelegateRows.module.css";

/** How long a finished delegate keeps its row — long enough to see it land. */
export const DEFAULT_DELEGATE_FADE_MS = 10 * 60 * 1000;

/**
 * Settings' "Fade out subagent cards after inactivity" (Config.delegate_fade)
 * as a window in ms: how long a finished delegate keeps its row, counted from
 * when it finished. Infinity is "Never"; absent is the 10-minute default.
 */
export function delegateFadeMs(fade?: DelegateFade): number {
  switch (fade) {
    case "0m":
      return 0;
    case "never":
      return Infinity;
    default:
      return DEFAULT_DELEGATE_FADE_MS;
  }
}

/** Rows shown before the rest fold behind "+N more". */
export const DELEGATE_ROWS_SHOWN = 3;

/**
 * The delegates still worth a row: every running one, plus finished ones
 * whose end is within `fadeMs` of now (delegateFadeMs). Running first, oldest
 * start first; then finished, most recently ended first — under "Never" a
 * session's history piles up behind "+N more", and what is running or just
 * ended must not be what gets folded away.
 */
export function visibleDelegates(delegates: Delegate[], now: number, fadeMs = DEFAULT_DELEGATE_FADE_MS): Delegate[] {
  const ended = (d: Delegate) => (d.ended_at ? Date.parse(d.ended_at) : NaN);
  const running = delegates
    .filter((d) => d.state === "running")
    .sort((a, b) => a.started_at.localeCompare(b.started_at));
  const finished = delegates
    .filter((d) => {
      if (d.state === "running") return false;
      if (fadeMs === Infinity) return true;
      const end = ended(d);
      return Number.isFinite(end) && now - end < fadeMs;
    })
    .sort((a, b) => (ended(b) || 0) - (ended(a) || 0));
  return [...running, ...finished];
}

/**
 * A session's delegates — harness-run subagents (docs/DELEGATES-DESIGN.md) —
 * nested under its card. Deliberately not a linked-session row: the dashed
 * trunk and plain rows say "this session's own work, run by its agent", where
 * a link's solid curve and arrowhead say "another session mads connected".
 * Clicking one opens it in the main pane.
 */
export function DelegateRows({
  delegates,
  selectedHarnessId,
  onOpen,
  fadeMs = DEFAULT_DELEGATE_FADE_MS,
}: {
  delegates: Delegate[];
  selectedHarnessId?: string;
  onOpen?: (d: Delegate) => void;
  /** How long a finished delegate keeps its row (delegateFadeMs). */
  fadeMs?: number;
}) {
  const now = useRelativeNow();
  const [expanded, setExpanded] = useState(false);
  const visible = visibleDelegates(delegates, now, fadeMs);
  if (visible.length === 0) return null;
  const shown = expanded ? visible : visible.slice(0, DELEGATE_ROWS_SHOWN);
  const hidden = visible.length - shown.length;
  return (
    <div className={styles.delegates} data-testid="delegate-rows">
      {shown.map((d) => (
        <button
          key={d.harness_id}
          type="button"
          className={styles.row}
          data-selected={d.harness_id === selectedHarnessId || undefined}
          data-state={d.state}
          title={rowTitle(d)}
          onClick={() => onOpen?.(d)}
        >
          <KindIcon kind="agent" size="sm" />
          <span className={styles.name}>{d.description || d.type || "Subagent"}</span>
          <span className={styles.meta}>
            {d.state === "running" ? <LiveElapsed since={d.started_at} /> : <Duration d={d} />}
            {d.state === "running" ? <Spinner /> : <StatusGlyph status={glyphFor(d)} />}
          </span>
        </button>
      ))}
      {visible.length > DELEGATE_ROWS_SHOWN && (
        <button type="button" className={styles.more} onClick={() => setExpanded((e) => !e)}>
          {expanded ? "Show fewer" : `+${hidden} more`}
        </button>
      )}
    </div>
  );
}

function LiveElapsed({ since }: { since: string }) {
  const now = useElapsedNow();
  const start = Date.parse(since);
  if (!Number.isFinite(start)) return null;
  return <span className={styles.elapsed}>{formatElapsed(now - start)}</span>;
}

function Duration({ d }: { d: Delegate }) {
  const ms =
    d.duration_ms ?? (d.ended_at ? Date.parse(d.ended_at) - Date.parse(d.started_at) : NaN);
  if (!Number.isFinite(ms) || ms < 0) return null;
  return <span className={styles.elapsed}>{formatMs(ms)}</span>;
}

function glyphFor(d: Delegate) {
  if (d.state === "completed") return "completed" as const;
  if (d.state === "failed") return "failed" as const;
  return "interrupted" as const;
}

// The type ("Explore") lives here and in the full view, not on the row: the
// rail is narrow, and the name is what tells two delegates apart.
function rowTitle(d: Delegate): string {
  const name = d.description && d.type ? `${d.description} (${d.type})` : d.description || d.type || "Subagent";
  if (d.state === "running") return `${name} — running${d.backgrounded ? " in the background" : ""}`;
  const why = d.reason ? ` (${d.reason.replace("_", " ")})` : "";
  return `${name} — ${d.state}${why}${d.inferred ? ", concluded by mads" : ""}`;
}
