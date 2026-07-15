import { dotState } from "../../sessions/rail/railGroups";
import type { Session } from "../../../router/types";

/** Reuses the rail's own `dotState` so the badge and the amber dot never drift apart. */
export function attentionCount(
  sessions: Session[],
  openTaskCounts: Record<string, number>,
): number {
  return sessions.filter((s) => dotState(s, openTaskCounts[s.id] ?? 0) === "attn").length;
}
