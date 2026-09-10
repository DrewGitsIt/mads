import type { TaskModel } from "./conversationItems";
import { KindIcon, Spinner } from "./primitives";
import { taskCounters } from "./TaskCard";
import styles from "./Substrip.module.css";

export interface SubstripEntry {
  model: TaskModel;
  /** Current-op ticker line — latest subagent tool title / "Thinking". */
  op?: string;
}

/**
 * Pinned substrip (T15): while any subagent task runs, one strip per task
 * sits above the composer with the task name, a current-op ticker and live
 * counters. Click opens the subagent's full stream in the dock.
 */
export function Substrip({
  entries,
  onOpen,
}: {
  entries: SubstripEntry[];
  onOpen: (model: TaskModel) => void;
}) {
  if (entries.length === 0) return null;
  return (
    <div data-testid="substrip">
      {entries.map(({ model, op }) => (
        <button
          key={model.taskId}
          type="button"
          className={styles.strip}
          data-dock-source
          title="Open subagent stream"
          onClick={() => onOpen(model)}
        >
          <KindIcon kind="agent" />
          <span className={styles.name}>{model.description ?? model.subagentType ?? "Subagent"}</span>
          {op && (
            <span className={styles.op} key={op}>
              {op}
            </span>
          )}
          <span className={styles.counters}>
            <span>{taskCounters(model)}</span>
            <Spinner />
          </span>
        </button>
      ))}
    </div>
  );
}
