import { useState } from "react";
import type { Delegate } from "../../../router/types";
import { KindIcon, Spinner, StatusGlyph } from "../../../conversation/activity/primitives";
import { formatElapsed, formatMs } from "../../../conversation/activity/toolDisplay";
import { useElapsedNow, useRelativeNow } from "../../../utils/relativeTime";
import styles from "./DelegateRows.module.css";

/** How long a finished delegate keeps its row — long enough to see it land. */
export const RECENT_DELEGATE_MS = 2 * 60 * 1000;

/** Rows shown before the rest fold behind "+N more". */
export const DELEGATE_ROWS_SHOWN = 3;

/**
 * The delegates still worth a row: every running one, plus finished ones
 * whose end is within RECENT_DELEGATE_MS of now. Oldest start first.
 */
export function visibleDelegates(delegates: Delegate[], now: number): Delegate[] {
  return delegates.filter((d) => {
    if (d.state === "running") return true;
    const ended = d.ended_at ? Date.parse(d.ended_at) : NaN;
    return Number.isFinite(ended) && now - ended < RECENT_DELEGATE_MS;
  });
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
}: {
  delegates: Delegate[];
  selectedHarnessId?: string;
  onOpen?: (d: Delegate) => void;
}) {
  const now = useRelativeNow();
  const [expanded, setExpanded] = useState(false);
  const visible = visibleDelegates(delegates, now);
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
