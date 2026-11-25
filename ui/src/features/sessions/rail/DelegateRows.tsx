import { useState } from "react";
import type { Delegate, DelegateFade } from "../../../router/types";
import { isLiveDelegate, isWaitingDelegate, waitingOnText } from "../../../router/delegateLive";
import { Spinner, StatusGlyph } from "../../../conversation/activity/primitives";
import { formatElapsed, formatMs } from "../../../conversation/activity/toolDisplay";
import { useElapsedNow, useRelativeNow } from "../../../utils/relativeTime";
import { WaitingGlyph } from "./StatusMark";
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

/** Top-level rows shown before the rest fold behind "+N more". */
export const DELEGATE_ROWS_SHOWN = 3;

const endedAt = (d: Delegate) => (d.ended_at ? Date.parse(d.ended_at) : NaN);

/** Whether a finished delegate is still inside the fade window. */
function withinFade(d: Delegate, now: number, fadeMs: number): boolean {
  if (fadeMs === Infinity) return true;
  const end = endedAt(d);
  return Number.isFinite(end) && now - end < fadeMs;
}

/**
 * Live ones first (running or waiting — isLiveDelegate), oldest start first;
 * then finished, most recently ended first — under "Never" a session's
 * history piles up behind "+N more", and what is running or just ended must
 * not be what gets folded away.
 */
function orderDelegates(list: Delegate[]): Delegate[] {
  const live = list.filter(isLiveDelegate).sort((a, b) => a.started_at.localeCompare(b.started_at));
  const finished = list.filter((d) => !isLiveDelegate(d)).sort((a, b) => (endedAt(b) || 0) - (endedAt(a) || 0));
  return [...live, ...finished];
}

/**
 * The delegates still worth a top-level row: every live one, plus finished
 * ones whose end is within `fadeMs` of now (delegateFadeMs), in
 * orderDelegates' order. A waiting delegate counts as live: its turn ended,
 * but it isn't done, so it never fades.
 */
export function visibleDelegates(delegates: Delegate[], now: number, fadeMs = DEFAULT_DELEGATE_FADE_MS): Delegate[] {
  return orderDelegates(delegates.filter((d) => isLiveDelegate(d) || withinFade(d, now, fadeMs)));
}

/** One row of the tree, with the rows nested under it. */
export interface DelegateNode {
  d: Delegate;
  /**
   * Its finished subagents that aren't shown, folded into one
   * "N subagents finished" line placed first under it. Empty when none —
   * or once the user has unfolded them, when they are `children` instead.
   */
  folded: Delegate[];
  children: DelegateNode[];
}

export interface DelegateTree {
  /** Top-level rows to draw, each with what nests under it. */
  roots: DelegateNode[];
  /** Top-level rows behind "+N more" (their nested rows go with them). */
  hidden: number;
  /** Every top-level row there is, shown or hidden — whether "Show fewer" has anything to fold. */
  total: number;
}

/**
 * A session's delegates as the rail draws them (docs/mockups/nested-delegates.html):
 * each nested under the delegate that spawned it (parent_harness_id).
 *
 * - Top-level rows follow the fade rules (visibleDelegates).
 * - A nested row shows only while it is live, when it is the open one, or
 *   when its parent is the open one — then that parent's finished subagents
 *   are listed in full (theirs, in turn, only while live). Otherwise a
 *   parent's finished subagents fold into one line, until unfolded
 *   (`unfolded`, by parent harness id).
 * - A shown row keeps its parent's row, and that parent's, so it always has
 *   a home: a live subagent under a parent that has faded out still sits
 *   where it was spawned.
 * - "+N more" counts top-level rows only; a row's subagents come with it,
 *   and the open delegate's branch is always kept.
 *
 * A parent the list doesn't know (never heard, or pruned) makes the row
 * top-level, as does a parent chain that loops back on itself — a bad
 * record must not make a row vanish.
 */
export function delegateTree(
  delegates: Delegate[],
  now: number,
  {
    fadeMs = DEFAULT_DELEGATE_FADE_MS,
    selectedHarnessId,
    unfolded,
    expanded = false,
  }: { fadeMs?: number; selectedHarnessId?: string; unfolded?: ReadonlySet<string>; expanded?: boolean } = {},
): DelegateTree {
  const byId = new Map(delegates.map((d) => [d.harness_id, d]));
  const chainOf = (d: Delegate): string[] | undefined => {
    // Its ancestors, nearest first; undefined when the chain loops.
    const out: string[] = [];
    const seen = new Set([d.harness_id]);
    let p = d.parent_harness_id ? byId.get(d.parent_harness_id) : undefined;
    while (p) {
      if (seen.has(p.harness_id)) return undefined;
      seen.add(p.harness_id);
      out.push(p.harness_id);
      p = p.parent_harness_id ? byId.get(p.parent_harness_id) : undefined;
    }
    return out;
  };
  const ancestors = new Map<string, string[]>();
  for (const d of delegates) ancestors.set(d.harness_id, chainOf(d) ?? []);
  const parentOf = (d: Delegate) => ancestors.get(d.harness_id)?.[0];

  const shown = new Set<string>();
  for (const d of delegates) {
    const parent = parentOf(d);
    if (d.harness_id === selectedHarnessId) shown.add(d.harness_id);
    else if (isLiveDelegate(d)) shown.add(d.harness_id);
    else if (!parent ? withinFade(d, now, fadeMs) : parent === selectedHarnessId) shown.add(d.harness_id);
  }
  for (const id of [...shown]) for (const a of ancestors.get(id) ?? []) shown.add(a);

  const kids = new Map<string, Delegate[]>();
  for (const d of delegates) {
    const parent = parentOf(d);
    if (parent) kids.set(parent, [...(kids.get(parent) ?? []), d]);
  }
  const node = (d: Delegate): DelegateNode => {
    const mine = kids.get(d.harness_id) ?? [];
    // Everything under it that isn't shown is finished: live ones, the open
    // one and the open one's children all are.
    let folded = orderDelegates(mine.filter((k) => !shown.has(k.harness_id)));
    let rows = mine.filter((k) => shown.has(k.harness_id));
    if (folded.length > 0 && unfolded?.has(d.harness_id)) {
      rows = [...rows, ...folded];
      folded = [];
    }
    return { d, folded, children: orderDelegates(rows).map(node) };
  };

  const top = orderDelegates(delegates.filter((d) => !parentOf(d) && shown.has(d.harness_id)));
  let kept = top;
  if (!expanded) {
    // The open delegate's branch: its top-level ancestor, or itself.
    const open = selectedHarnessId ? byId.get(selectedHarnessId) : undefined;
    const openRoot = open && shown.has(open.harness_id) ? (ancestors.get(open.harness_id)?.at(-1) ?? open.harness_id) : undefined;
    kept = top.filter((d, i) => i < DELEGATE_ROWS_SHOWN || d.harness_id === openRoot);
  }
  return { roots: kept.map(node), hidden: top.length - kept.length, total: top.length };
}

/**
 * A session's delegates — harness-run subagents (docs/DELEGATES-DESIGN.md) —
 * nested under its card, each subagent's own under it (delegateTree).
 * Deliberately not a linked-session row: a hairline and plain rows say "this
 * session's own work, run by its agent", where a link's heavy curve and
 * arrowhead say "another session mads connected". Clicking one opens it in
 * the main pane.
 */
export function DelegateRows({
  delegates,
  selectedHarnessId,
  onOpen,
  fadeMs = DEFAULT_DELEGATE_FADE_MS,
  linkTrunk = false,
}: {
  delegates: Delegate[];
  selectedHarnessId?: string;
  onOpen?: (d: Delegate) => void;
  /** How long a finished delegate keeps its row (delegateFadeMs). */
  fadeMs?: number;
  /** Draw the holder's link trunk past these rows: they sit between it and
   *  its linked subjects. */
  linkTrunk?: boolean;
}) {
  const now = useRelativeNow();
  const [expanded, setExpanded] = useState(false);
  // Parents whose "N subagents finished" line was clicked open. Local and
  // forgotten on reload: it is a peek, not a preference.
  const [unfolded, setUnfolded] = useState<ReadonlySet<string>>(() => new Set());
  const tree = delegateTree(delegates, now, { fadeMs, selectedHarnessId, unfolded, expanded });
  if (tree.total === 0) return null;
  const unfold = (id: string) => setUnfolded((cur) => new Set(cur).add(id));
  return (
    <div className={styles.delegates} data-testid="delegate-rows" data-link-trunk={linkTrunk || undefined}>
      {tree.roots.map((n) => (
        <DelegateBranch key={n.d.harness_id} node={n} selectedHarnessId={selectedHarnessId} onOpen={onOpen} onUnfold={unfold} />
      ))}
      {(expanded ? tree.total > DELEGATE_ROWS_SHOWN : tree.hidden > 0) && (
        <button type="button" className={styles.more} onClick={() => setExpanded((e) => !e)}>
          {expanded ? "Show fewer" : `+${tree.hidden} more`}
        </button>
      )}
    </div>
  );
}

/** One row, then — one level in — its folded finished subagents' line first, and its shown subagents' rows. */
function DelegateBranch({
  node,
  selectedHarnessId,
  onOpen,
  onUnfold,
}: {
  node: DelegateNode;
  selectedHarnessId?: string;
  onOpen?: (d: Delegate) => void;
  onUnfold: (parentHarnessId: string) => void;
}) {
  const { d, folded, children } = node;
  const waiting = isWaitingDelegate(d);
  return (
    <>
      <button
        type="button"
        className={styles.row}
        data-selected={d.harness_id === selectedHarnessId || undefined}
        data-state={waiting ? "waiting" : d.state}
        title={rowTitle(d)}
        onClick={() => onOpen?.(d)}
      >
        <span className={styles.glyph}>
          {d.state === "running" ? <Spinner /> : waiting ? <WaitingGlyph /> : <StatusGlyph status={glyphFor(d)} />}
        </span>
        <span className={styles.name}>{d.description || d.type || "Subagent"}</span>
        <span className={styles.meta}>
          {d.state === "running" ? (
            <LiveElapsed since={d.started_at} priorMs={d.prior_duration_ms} />
          ) : waiting ? (
            // No number: how long it has waited isn't how long it has worked,
            // and it is not working. What it waits on is in the tooltip.
            <span className={styles.elapsed}>waiting</span>
          ) : (
            <Duration d={d} />
          )}
        </span>
      </button>
      {(folded.length > 0 || children.length > 0) && (
        <div className={styles.nested} data-testid="delegate-nested">
          {folded.length > 0 && (
            <button type="button" className={styles.fold} onClick={() => onUnfold(d.harness_id)}>
              {folded.length === 1 ? "1 subagent finished" : `${folded.length} subagents finished`} ▸
            </button>
          )}
          {children.map((c) => (
            <DelegateBranch
              key={c.d.harness_id}
              node={c}
              selectedHarnessId={selectedHarnessId}
              onOpen={onOpen}
              onUnfold={onUnfold}
            />
          ))}
        </div>
      )}
    </>
  );
}

/** The live run's clock, on top of a resumed delegate's earlier runs. */
function LiveElapsed({ since, priorMs = 0 }: { since: string; priorMs?: number }) {
  const now = useElapsedNow();
  const start = Date.parse(since);
  if (!Number.isFinite(start)) return null;
  return <span className={styles.elapsed}>{formatElapsed(priorMs + now - start)}</span>;
}

/** Total time across its runs: the harness's, else the latest run's span plus the earlier runs'. */
function Duration({ d }: { d: Delegate }) {
  const ms =
    d.duration_ms ??
    (d.ended_at ? Date.parse(d.ended_at) - Date.parse(d.started_at) + (d.prior_duration_ms ?? 0) : NaN);
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
  if (d.waiting_on) return `${name} — turn ended, waiting on ${waitingOnText(d.waiting_on)}`;
  const why = d.reason ? ` (${d.reason.replace("_", " ")})` : "";
  return `${name} — ${d.state}${why}${d.inferred ? ", concluded by mads" : ""}`;
}
