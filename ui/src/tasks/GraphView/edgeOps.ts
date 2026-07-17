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

/**
 * Connect: bottom (source) handle of `from` dragged to top (target) handle
 * of `to` means "to depends on from" (dagre TB ranks `from` above `to`,
 * `buildEdges` already encodes edges as {from: depId, to: dependentId}).
 * Returns null for a no-op: self-loop, or the dependency already exists
 * (task_deps has PRIMARY KEY (task_id, depends_on) — a duplicate INSERT in
 * the same PUT payload would 500, so this guard is load-bearing, not
 * cosmetic).
 */
export function planEdgeCreation(
  from: string,
  to: string,
  tasksById: Record<string, Task>,
): EdgeStripPlan | null {
  const target = tasksById[to];
  if (!target || from === to) return null;
  const deps = target.dependencies ?? [];
  if (deps.includes(from)) return null;
  return { updates: [{ task: target, nextDependencies: [...deps, from] }] };
}

/**
 * Remove: one entry per deleted edge (Backspace can multi-select and delete
 * more than one edge at once — the single-edge X-button click is just a
 * one-element call of the same fn).
 */
export function planEdgesRemoval(
  pairs: { from: string; to: string }[],
  tasksById: Record<string, Task>,
): EdgeStripPlan {
  const updates: EdgeStripPlan["updates"] = [];
  for (const { from, to } of pairs) {
    const target = tasksById[to];
    if (!target) continue;
    updates.push({ task: target, nextDependencies: (target.dependencies ?? []).filter((d) => d !== from) });
  }
  return { updates };
}
