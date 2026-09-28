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
 * Active sessions always show a live dot regardless of open task count;
 * Recent sessions show amber ("attn") only when they carry *blocked* work —
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

export interface RailGroups {
  active: Session[];
  recent: Session[];
}

export function groupSessions(sessions: Session[]): RailGroups {
  const active = sessions
    .filter((s) => s.activity === "active")
    .sort((a, b) => b.updated_at.localeCompare(a.updated_at));
  const recent = sessions
    .filter((s) => s.activity !== "active")
    .sort((a, b) => b.updated_at.localeCompare(a.updated_at));
  return { active, recent };
}
