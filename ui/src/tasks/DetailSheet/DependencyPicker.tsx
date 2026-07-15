import type { Task } from "../../router/types";
import styles from "./DependencyPicker.module.css";

export interface DependencyPickerProps {
  task: Task;
  tasksById: Record<string, Task>;
  sessionTasks: Task[];
  dependencies: string[];
  onChange: (dependencies: string[]) => void;
}

/**
 * Cheap, non-authoritative "would create a cycle" heads-up: a DFS from the
 * candidate forward through its own dependents, checking whether it reaches
 * back to `task`. No network call — the real check is GET /tasks/cycles
 * after save (§5). Don't conflate this with GraphView's authoritative
 * server-side cycle highlighting.
 */
function wouldCreateCycle(
  task: Task,
  candidateId: string,
  dependencies: string[],
  tasksById: Record<string, Task>,
): boolean {
  if (candidateId === task.id) return true;
  const visited = new Set<string>();
  const stack = [candidateId];
  while (stack.length > 0) {
    const current = stack.pop()!;
    if (current === task.id) return true;
    if (visited.has(current)) continue;
    visited.add(current);
    stack.push(...(tasksById[current]?.dependencies ?? []));
  }
  return false;
}

export function DependencyPicker({
  task,
  tasksById,
  sessionTasks,
  dependencies,
  onChange,
}: DependencyPickerProps) {
  const candidates = sessionTasks.filter((t) => t.id !== task.id);

  function toggle(candidateId: string) {
    if (dependencies.includes(candidateId)) {
      onChange(dependencies.filter((id) => id !== candidateId));
    } else {
      onChange([...dependencies, candidateId]);
    }
  }

  return (
    <div className={styles.picker}>
      {candidates.map((candidate) => {
        const checked = dependencies.includes(candidate.id);
        const warnCycle =
          !checked && wouldCreateCycle(task, candidate.id, dependencies, tasksById);
        return (
          <label key={candidate.id} className={styles.row}>
            <input
              type="checkbox"
              checked={checked}
              onChange={() => toggle(candidate.id)}
            />
            <span className={styles.title}>{candidate.title}</span>
            {warnCycle && (
              <span className={styles.warning}>would create a cycle</span>
            )}
          </label>
        );
      })}
    </div>
  );
}
