import type { Session } from "../../../router/types";

export type DotState = "on" | "attn" | "off";

/**
 * Active sessions always show a live dot regardless of open task count;
 * Recent sessions show amber ("attn") only when they carry open work.
 * Mirrors docs/ux-prototype.html's `.dot on|attn|off` markup (session rail).
 */
export function dotState(session: Session, openTaskCount: number): DotState {
  if (session.activity === "active") return "on";
  return openTaskCount > 0 ? "attn" : "off";
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
