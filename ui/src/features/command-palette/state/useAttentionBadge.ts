import { useEffect, useMemo } from "react";
import { attentionCount } from "./attentionCount";
import { getBadgeBridge } from "../badgeBridge";
import type { OpenTaskBreakdown } from "../../sessions/rail/railGroups";
import type { Session } from "../../../router/types";

/** Derives the attention count and pushes it to the Electron dock badge whenever it changes. */
export function useAttentionBadge(
  sessions: Session[],
  openTaskCounts: Record<string, OpenTaskBreakdown>,
): number {
  const count = useMemo(() => attentionCount(sessions, openTaskCounts), [sessions, openTaskCounts]);

  useEffect(() => {
    getBadgeBridge()?.setCount(count);
  }, [count]);

  return count;
}
