import type { Session } from "../../../router/types";

export type DotState = "on" | "attn" | "off";

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
