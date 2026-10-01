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
 * stream by useSessionAgentState() over the mirror on each session record.
 */
export interface SessionAgentState {
  /** The session's child exited unexpectedly and nothing has been heard since. */
  crashed?: boolean;
  /** Set while a permission park is unresolved — the instant it parked. */
  awaitingSince?: string;
  /** The parked call's tool, when the request named one. */
  awaitingTool?: string;
  /** Spawned subagents still running, backgrounded or not. */
  subagents: number;
  /**
   * Of those, the ones the main agent is actually waiting on — a foreground
   * Agent call blocks the turn, a backgrounded one does not. Only a blocking
   * sidechain gets to speak for the session on the status line; see statusOf.
   *
   * Absent means "assume they all block", which is what every subagent was
   * taken to do before backgrounding was distinguished — so a producer that
   * doesn't know about the flag keeps the older, louder behaviour rather than
   * silently going quiet.
   */
  blockingSubagents?: number;
  /**
   * Set while a turn is in flight — the instant the TURN went in flight, held
   * across the turn's label changes, so the counter spans the whole turn.
   */
  workingSince?: string;
  /**
   * The server-built clause for what the agent is doing right now ("Running
   * bash · make check", "Thinking…"). Only ever set alongside workingSince.
   */
  label?: string;
  /** Sticky: a turn ended and the session hasn't been opened since. */
  done?: boolean;
}

export const EMPTY_AGENT_STATE: SessionAgentState = { subagents: 0 };

export type RailStatusKind =
  | "crashed"
  | "needs"
  | "working"
  | "stalled"
  | "done"
  | "ready"
  | "sleeping";

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
 * How long a "working" claim stays credible. Past this the card stops
 * animating and says the agent has gone quiet instead.
 *
 * The bound is the product's own longest turn, not a guess: 30 minutes is
 * DefaultPipelineStageTimeoutMinutes (pkg/contracts/config.go) — what mads
 * itself is willing to wait for one agent turn before calling it failed. A
 * claim older than that is older than any turn this system would let run.
 *
 * It exists because nothing else can retire the claim. agent_state is written
 * by the Runner on transitions only (services/session/internal/runner/
 * agentstate.go), so the last thing a wedged or hard-killed agent wrote stays
 * on the record forever: useSessionAgentState's mirroredWorking() already
 * refuses a working mirror on an *inactive* session, but a session whose
 * service is alive while the agent underneath it is not stays `active`, and
 * the rail spun a "Thinking…" that had been true 23 hours earlier (observed
 * 2026-10-01, two sessions, one with no provider child left at all).
 *
 * This is presentation, not repair — the underlying session is still wedged
 * and still needs closing. It is here because the rail must not state as
 * present tense something it cannot support.
 */
export const WORKING_STALE_AFTER_MS = 30 * 60 * 1000;

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
  now: number = Date.now(),
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
  if (state.workingSince !== undefined && isStaleWorking(state.workingSince, now)) {
    // Deliberately keeps `since`, so the counter beside it reads "23h" — the
    // age of the claim is the whole evidence for not believing it.
    return {
      kind: "stalled",
      tone: "needs",
      dot: "attn",
      // A spinner that stopped. No animation: the one state that moves is the
      // one that is actually moving.
      glyph: "◌",
      label: "Stalled · no agent activity",
      since: state.workingSince,
    };
  }
  if (state.workingSince !== undefined) {
    return {
      kind: "working",
      tone: "working",
      dot: "work",
      // No glyph: this is the one state that spins.
      label:
        (state.blockingSubagents ?? state.subagents) > 0
          ? // A sidechain's tools are the subagent's activity, not the main
            // agent's: they roll up as a count, which is why the runner skips
            // them when it builds the label below. Only a *blocking* sidechain
            // earns the line, though — while the main agent waits on one it
            // has nothing of its own to report, so the count is the better
            // clause. A backgrounded subagent leaves the main agent working,
            // and those can outlive many turns: letting one own this line
            // pinned every card that spawned one to a single sentence for
            // minutes at a stretch while the label underneath changed
            // constantly. The count still reaches the tooltip.
            `${plural(state.subagents, "subagent")} running`
          : // The server-built clause — the same enrichment the open
            // conversation's working-indicator ticker shows for the same call,
            // so the two surfaces cannot disagree. "Working…" is the fallback
            // for a turn whose first label hasn't reached this browser yet.
            (state.label ?? "Working…"),
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
 * Whether a working claim has outlived the longest turn mads would run (see
 * WORKING_STALE_AFTER_MS). An unparseable stamp is never stale — a bad value
 * is not evidence of a dead agent, and guessing from one would turn every
 * clock-skewed record into a false alarm.
 */
export function isStaleWorking(workingSince: string, now: number): boolean {
  const since = Date.parse(workingSince);
  return Number.isFinite(since) && now - since >= WORKING_STALE_AFTER_MS;
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
  // Stalled joins the attention set: a wedged agent is not going to recover on
  // its own, and closing it is the user's job.
  if (status.kind === "crashed" || status.kind === "needs" || status.kind === "stalled") {
    return "needs";
  }
  if (status.kind === "working") return "working";
  return status.dot === "attn" ? "needs" : "idle";
}

/**
 * When this session last replied — the rail's one recency value, shared by the
 * card's stamp, the order (railOrder.ts) and the "Earlier" fold below.
 *
 * last_turn_at is the only field that means "the agent answered": updated_at is
 * bumped by renames, summary writes and activity flips too, which is what used
 * to move a card in the list when it was merely auto-titled. created_at is the
 * fallback for a session that has never replied — immutable, so nothing a
 * record write does can move that card either. Deliberately NOT what the card
 * prints: a created_at shown as "last reply" would be the same lie in reverse
 * (SessionRow prints "no reply yet" instead).
 */
export function lastReplyKey(session: Session): string {
  return session.last_turn_at ?? session.created_at;
}

/** A session falls into the collapsed "Earlier" tail once its last reply is a day old. */
export const EARLIER_AFTER_MS = 24 * 60 * 60 * 1000;

export function isEarlier(session: Session, now: number): boolean {
  const t = Date.parse(lastReplyKey(session));
  return !Number.isNaN(t) && now - t >= EARLIER_AFTER_MS;
}
