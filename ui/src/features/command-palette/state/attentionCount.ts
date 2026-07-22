import { EMPTY_OPEN_TASK_BREAKDOWN, dotState } from "../../sessions/rail/railGroups";
import type { OpenTaskBreakdown } from "../../sessions/rail/railGroups";
import type { Session } from "../../../router/types";

/** Reuses the rail's own `dotState` so the badge and the amber dot never drift apart. */
export function attentionCount(
  sessions: Session[],
  openTaskCounts: Record<string, OpenTaskBreakdown>,
): number {
  return sessions.filter((s) => dotState(s, openTaskCounts[s.id] ?? EMPTY_OPEN_TASK_BREAKDOWN) === "attn")
    .length;
}
