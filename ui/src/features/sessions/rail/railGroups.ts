import type { Session } from "../../../router/types";

export type DotState = "on" | "attn" | "off" | "bad" | "work";

/** Per-session open work, bucketed by status. needs_review folds into `ready` (same simplification as GraphView's graphStatus.ts). */
export interface OpenTaskBreakdown {
  running: number;
  ready: number;
  blocked: number;
}

export const EMPTY_OPEN_TASK_BREAKDOWN: OpenTaskBreakdown = { running: 0, ready: 0, blocked: 0 };

export function totalOpenTasks(breakdown: OpenTaskBreakdown): number {
  return breakdown.running + breakdown.ready + breakdown.blocked;
}

/**
 * Live sessions always show a live dot regardless of open task count;
 * sleeping sessions show amber ("attn") only when they carry *blocked* work —
 * running/ready work is expected, calm progress, not something needing eyes.
 * Mirrors docs/ux-prototype.html's `.dot on|attn|off` markup (session rail).
 */
export function dotState(session: Session, breakdown: OpenTaskBreakdown): DotState {
  if (session.activity === "active") return "on";
  return breakdown.blocked > 0 ? "attn" : "off";
}

/**
 * Per-session live agent state, folded from the control-plane session.*
 * stream by useSessionAgentState(). Phase 0 of the status line: every field
 * here is derivable from events that stream already carries.
 */
export interface SessionAgentState {
  /** The session's child exited unexpectedly and nothing has been heard since. */
  crashed?: boolean;
  /** Set while a permission park is unresolved — the instant it parked. */
  awaitingSince?: string;
  /** The parked call's tool, when the request named one. */
  awaitingTool?: string;
  /** Spawned subagents still running. */
  subagents: number;
  /** Set while a turn is in flight — the instant it went in flight. */
  workingSince?: string;
  /** Sticky: a turn ended and the session hasn't been opened since. */
  done?: boolean;
}

export const EMPTY_AGENT_STATE: SessionAgentState = { subagents: 0 };

export type RailStatusKind = "crashed" | "needs" | "working" | "done" | "ready" | "sleeping";

/** Maps 1:1 onto the mockup's `.status.bad|needs|working|done|idle` tones. */
export type RailStatusTone = "bad" | "needs" | "working" | "done" | "idle";

export interface RailStatus {
  kind: RailStatusKind;
  tone: RailStatusTone;
  dot: DotState;
  /** One clause, ellipsis-truncated by the card — never wrapped, so height is fixed. */
  label: string;
  /** The decorative leading mark. Absent means "draw the spinner instead". */
  glyph?: string;
  /** When the state began, for the elapsed counter. Absent means no counter. */
  since?: string;
}

function plural(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? "" : "s"}`;
}

/**
 * The card's second line (mockup docs/mockups/sessions-ux-v2.html §1), in the
 * mockup's precedence: the first state that applies wins, so an approval park
 * always beats "working" and a crash always beats everything.
 *
 * The three calm states defer to dotState() for their dot rather than hard-
 * coding the mockup's on/off, so a sleeping session with blocked work keeps
 * today's amber puck — which is also what the command palette's attention
 * badge counts (attentionCount.ts), and the two must not disagree.
 */
export function statusOf(
  session: Session,
  state: SessionAgentState,
  breakdown: OpenTaskBreakdown,
): RailStatus {
  if (state.crashed) {
    return {
      kind: "crashed",
      tone: "bad",
      dot: "bad",
      glyph: "✕",
      label:
        breakdown.blocked > 0
          ? `Crashed · ${plural(breakdown.blocked, "blocked task")}`
          : "Crashed · reopen to restart",
    };
  }
  if (state.awaitingSince !== undefined) {
    return {
      kind: "needs",
      tone: "needs",
      dot: "attn",
      glyph: "▲",
      label: state.awaitingTool ? `Needs approval · ${state.awaitingTool}` : "Needs approval",
      since: state.awaitingSince,
    };
  }
  if (state.workingSince !== undefined) {
    return {
      kind: "working",
      tone: "working",
      dot: "work",
      // No glyph: this is the one state that spins.
      label:
        state.subagents > 0
          ? `${plural(state.subagents, "subagent")} running`
          : // Phase 0 has no tool title and no thinking signal on the control
            // plane, so an in-flight turn says only that it is in flight.
            "Working…",
      since: state.workingSince,
    };
  }
  const dot = dotState(session, breakdown);
  // A turn that ended on a session that has since gone to sleep isn't news
  // any more — "Sleeping" is the truer line.
  if (state.done && session.activity === "active") {
    return { kind: "done", tone: "done", dot, glyph: "✓", label: "Done · your turn" };
  }
  if (session.activity === "active") {
    return { kind: "ready", tone: "idle", dot, glyph: "◦", label: "Ready" };
  }
  return { kind: "sleeping", tone: "idle", dot, glyph: "◦", label: "Sleeping · wakes on send" };
}

/**
 * The rail's filter chips (mockup docs/mockups/sessions-ux-v2.html §2, option
 * B). They replace the old Active/Recent grouping: a chip is a *badge you can
 * click*, so triage stops costing the list its stability — nothing moves when
 * a session's state changes, the counts just tick.
 */
export type RailFilter = "all" | "working" | "needs" | "idle";

/** Every filter but "all" — the bucket a single card falls in. */
export type RailFilterBucket = Exclude<RailFilter, "all">;

export const RAIL_FILTERS: { id: RailFilter; label: string }[] = [
  { id: "all", label: "All" },
  { id: "working", label: "Working" },
  { id: "needs", label: "Needs you" },
  { id: "idle", label: "Idle" },
];

/** Most urgent first — how a holder row folds in the subjects nested under it. */
const BUCKET_SEVERITY: Record<RailFilterBucket, number> = { needs: 0, working: 1, idle: 2 };

export function mostUrgentBucket(buckets: RailFilterBucket[]): RailFilterBucket {
  return buckets.reduce((a, b) => (BUCKET_SEVERITY[b] < BUCKET_SEVERITY[a] ? b : a), "idle");
}

/**
 * Which chip a card answers to, read off the very status line the card paints
 * — so a count can never disagree with the cards it claims to count.
 *
 * "Needs you" is the whole attention set: a parked approval, a crash, and also
 * a quiet session carrying blocked work (the amber puck dotState() already
 * draws, which is what the command palette's attention badge counts too).
 * Everything else that isn't mid-turn is Idle, live or sleeping alike —
 * "Done · your turn" included, since the turn it is reporting is over.
 */
export function filterBucketOf(status: RailStatus): RailFilterBucket {
  if (status.kind === "crashed" || status.kind === "needs") return "needs";
  if (status.kind === "working") return "working";
  return status.dot === "attn" ? "needs" : "idle";
}

/** A session falls into the collapsed "Earlier" tail once its last activity is a day old. */
export const EARLIER_AFTER_MS = 24 * 60 * 60 * 1000;

export function isEarlier(session: Session, now: number): boolean {
  const t = Date.parse(session.updated_at);
  return !Number.isNaN(t) && now - t >= EARLIER_AFTER_MS;
}
