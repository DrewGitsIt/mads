import type { Task } from "../../router/types";

/** Native HTML5 DnD payload key shared by UnsequencedTray (source) and GraphCanvas (drop target). */
export const TASK_ID_DRAG_TYPE = "application/x-mads-task-id";

/**
 * Mirrors the server's tray creation-order tie-break
 * (`ORDER BY created_at, id`, `services/data/internal/store/tasks.go:374`)
 * — don't rely on prop array order, `fixtureTasks` is deliberately not in
 * `created_at` order.
 */
export function trayOrder(tasks: Task[]): Task[] {
  return [...tasks].sort((a, b) =>
    a.created_at !== b.created_at ? a.created_at.localeCompare(b.created_at) : a.id.localeCompare(b.id),
  );
}
