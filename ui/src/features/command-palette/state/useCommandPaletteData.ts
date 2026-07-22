import { useOpenTaskCounts } from "../../sessions/state/useOpenTaskCounts";
import { useSessionsList } from "../../sessions/state/useSessionsList";
import { useAllTasks } from "./useAllTasks";
import type { OpenTaskBreakdown } from "../../sessions/rail/railGroups";
import type { Session, Task } from "../../../router/types";

export interface CommandPaletteData {
  sessions: Session[];
  tasks: Task[];
  openTaskCounts: Record<string, OpenTaskBreakdown>;
}

/**
 * Mounted once at App.tsx level, alongside the rail — not inside
 * CommandPalette.tsx, and not conditionally on `open`. Every fetch here
 * happens on mount/SSE event, never on palette open, so opening the palette
 * is provably free of new network activity.
 */
export function useCommandPaletteData(): CommandPaletteData {
  const { sessions } = useSessionsList();
  const openTaskCounts = useOpenTaskCounts();
  const tasks = useAllTasks();

  return { sessions, tasks, openTaskCounts };
}
