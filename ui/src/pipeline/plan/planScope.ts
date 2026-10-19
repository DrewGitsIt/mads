import type { Task } from "../../router/types";

// What a planned run will and won't do, before it starts. The engine only
// launches confirmed ready tasks whose dependencies all end up done; anything
// downstream of a task that can't finish in the run is reported as skipped.
// These helpers predict that from the task list and the Plan's include boxes.

type TasksById = Record<string, Task>;

// A dependency that can't become done in this run, on its own account: left
// out by the user, a draft, or neither ready nor already done (blocked, needs
// review, won't do — the engine only launches ready tasks).
function cutsOnItsOwn(dep: Task, excluded: ReadonlySet<string>): boolean {
  if (dep.status === "done") return false;
  return excluded.has(dep.id) || !!dep.draft || dep.status !== "ready";
}

/**
 * The dependency, at any depth, that keeps `task` from running in this plan:
 * the first one found that is excluded, a draft, or otherwise can't finish
 * (see above). Undefined when nothing upstream is in the way. Done
 * dependencies are satisfied and not looked past; ids that aren't in
 * `tasksById` are ignored. Doesn't consider `task` itself — check
 * `excluded.has(task.id)` / `task.draft` separately.
 */
export function leftOutBy(
  task: Task,
  tasksById: TasksById,
  excluded: ReadonlySet<string>,
  seen: Set<string> = new Set(),
): Task | undefined {
  for (const id of task.dependencies ?? []) {
    if (seen.has(id)) continue;
    seen.add(id);
    const dep = tasksById[id];
    if (!dep || dep.status === "done") continue;
    if (cutsOnItsOwn(dep, excluded)) return dep;
    const upstream = leftOutBy(dep, tasksById, excluded, seen);
    if (upstream) return upstream;
  }
  return undefined;
}

/**
 * The dependency, at any depth, that the run left out — excluded, or a
 * draft — so `task` can never run in it. Unlike leftOutBy, a dependency
 * that's merely not done yet (queued, running) isn't a cut: in a live run
 * that's a wait. Done dependencies aren't looked past.
 */
export function excludedUpstream(
  task: Task,
  tasksById: TasksById,
  excluded: ReadonlySet<string>,
  seen: Set<string> = new Set(),
): Task | undefined {
  for (const id of task.dependencies ?? []) {
    if (seen.has(id)) continue;
    seen.add(id);
    const dep = tasksById[id];
    if (!dep || dep.status === "done") continue;
    if (excluded.has(dep.id) || dep.draft) return dep;
    const upstream = excludedUpstream(dep, tasksById, excluded, seen);
    if (upstream) return upstream;
  }
  return undefined;
}

/** The tasks this plan will launch: ready, confirmed, included, and nothing upstream in the way. */
export function runnableInPlan(
  tasks: Task[],
  tasksById: TasksById,
  excluded: ReadonlySet<string>,
): Task[] {
  return tasks.filter(
    (t) =>
      t.status === "ready" &&
      !t.draft &&
      !excluded.has(t.id) &&
      leftOutBy(t, tasksById, excluded) === undefined,
  );
}

/**
 * The Plan's one warning line, or undefined: the ready tasks that won't run
 * because a dependency is left out (unticked, or a draft), and those
 * dependencies — "cli, docs won't run (depends on core lib)".
 */
export function planWarning(
  tasks: Task[],
  tasksById: TasksById,
  excluded: ReadonlySet<string>,
): string | undefined {
  const cut: Task[] = [];
  const by = new Map<string, Task>();
  for (const t of tasks) {
    if (t.status !== "ready" || t.draft || excluded.has(t.id)) continue;
    const dep = leftOutBy(t, tasksById, excluded);
    if (!dep || !(dep.draft || excluded.has(dep.id))) continue;
    cut.push(t);
    by.set(dep.id, dep);
  }
  if (cut.length === 0) return undefined;
  const names = (list: Task[]) => list.map((t) => t.title).join(", ");
  return `${names(cut)} won't run (depends on ${names([...by.values()])})`;
}

/**
 * The task's wave: 1 with no dependencies, else 1 + the deepest dependency's
 * wave. Counts every known dependency, whatever its status. A dependency
 * cycle is broken where it closes (the edge back into the cycle counts as
 * none), so this always terminates. Pass `memo` to share work across calls.
 */
export function waveOf(
  task: Task,
  tasksById: TasksById,
  memo: Map<string, number> = new Map(),
): number {
  return depth(task.id, tasksById, () => true, memo, new Set());
}

// Longest chain ending at `id`, through dependencies `include` accepts.
function depth(
  id: string,
  tasksById: TasksById,
  include: (id: string) => boolean,
  memo: Map<string, number>,
  onPath: Set<string>,
): number {
  const known = memo.get(id);
  if (known !== undefined) return known;
  onPath.add(id);
  let deepest = 0;
  for (const dep of tasksById[id]?.dependencies ?? []) {
    if (onPath.has(dep) || !tasksById[dep] || !include(dep)) continue;
    deepest = Math.max(deepest, depth(dep, tasksById, include, memo, onPath));
  }
  onPath.delete(id);
  memo.set(id, deepest + 1);
  return deepest + 1;
}

/**
 * The longest dependency chain among the tasks this plan will run, as ids in
 * run order (first wave first). Dependencies outside the runnable set (done,
 * left out) don't count toward it. Empty when nothing is runnable; ties go to
 * whichever chain is found first.
 */
export function criticalPath(
  tasks: Task[],
  tasksById: TasksById,
  excluded: ReadonlySet<string>,
): string[] {
  const runnable = new Set(runnableInPlan(tasks, tasksById, excluded).map((t) => t.id));
  const include = (id: string) => runnable.has(id);
  const memo = new Map<string, number>();
  let end: string | undefined;
  let longest = 0;
  for (const id of runnable) {
    const d = depth(id, tasksById, include, memo, new Set());
    if (d > longest) {
      longest = d;
      end = id;
    }
  }
  // Walk back from the end, each step to a dependency one wave shallower.
  const path: string[] = [];
  const onPath = new Set<string>();
  let current = end;
  while (current !== undefined) {
    path.push(current);
    onPath.add(current);
    const want = (memo.get(current) ?? 1) - 1;
    current = (tasksById[current]?.dependencies ?? []).find(
      (dep) => runnable.has(dep) && !onPath.has(dep) && memo.get(dep) === want,
    );
  }
  return path.reverse();
}
