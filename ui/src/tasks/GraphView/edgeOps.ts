import type { RouterClient } from "../../router/client";
import type { Task } from "../../router/types";

export interface EdgeStripPlan {
  updates: { task: Task; nextDependencies: string[] }[];
}

/**
 * Every affected task, both directions: the dragged node itself (-> []) and
 * every dependent that listed it. No separate "deps API" exists — writes
 * go through `RouterClient.updateTask`'s full-replace PUT, same idiom as
 * `TaskDetailSheet`/`DependencyPicker` already use.
 */
export function planEdgeStrip(taskId: string, tasksById: Record<string, Task>): EdgeStripPlan {
  const self = tasksById[taskId];
  const updates: EdgeStripPlan["updates"] = [];
  if ((self.dependencies ?? []).length > 0) {
    updates.push({ task: self, nextDependencies: [] });
  }
  for (const t of Object.values(tasksById)) {
    if (t.id === taskId) continue;
    if ((t.dependencies ?? []).includes(taskId)) {
      updates.push({ task: t, nextDependencies: (t.dependencies ?? []).filter((d) => d !== taskId) });
    }
  }
  return { updates };
}

export async function applyEdgeStrip(routerClient: RouterClient, plan: EdgeStripPlan): Promise<void> {
  await Promise.all(
    plan.updates.map(({ task, nextDependencies }) => routerClient.updateTask({ ...task, dependencies: nextDependencies })),
  );
}

/**
 * Built from the plan's own pre-strip snapshot (`task.dependencies`,
 * captured before the strip is applied) — no refetch needed to undo.
 */
export function invertPlan(plan: EdgeStripPlan): EdgeStripPlan {
  return { updates: plan.updates.map(({ task }) => ({ task, nextDependencies: task.dependencies ?? [] })) };
}
