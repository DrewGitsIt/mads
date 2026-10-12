import type { PlanTodo } from "./conversationItems";
import { KindIcon } from "./primitives";
import styles from "./PlanWidget.module.css";

const MARKS: Record<PlanTodo["status"], string> = {
  completed: "✓",
  in_progress: "▸",
  pending: "○",
};

/**
 * TodoWrite as a checklist (T14): done ✓ / current ▸ / pending ○, updated
 * in place — buildItems routes every later TodoWrite call back into this
 * same widget instead of emitting activity rows.
 */
export function PlanWidget({
  todos,
  findKey,
}: {
  todos: PlanTodo[];
  /** Searchable by find (useFind), as this item. */
  findKey?: string;
}) {
  const done = todos.filter((t) => t.status === "completed").length;
  return (
    <div className={styles.plan} data-testid="plan-widget" data-find={findKey}>
      <div className={styles.head} data-find-skip>
        <KindIcon kind="plan" />
        <span>Plan</span>
        <span className={styles.count}>
          {done} of {todos.length} · updates in place
        </span>
      </div>
      {todos.map((t, i) => (
        <div key={i} className={styles.item} data-status={t.status}>
          <span className={styles.mark} aria-hidden="true">
            {MARKS[t.status]}
          </span>
          <span className={styles.text}>{t.content}</span>
        </div>
      ))}
    </div>
  );
}
