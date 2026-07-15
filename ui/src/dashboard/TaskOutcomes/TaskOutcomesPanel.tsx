import { useState } from "react";
import type { TaskOutcomes, TaskStatus } from "../../router/types";
import styles from "./TaskOutcomesPanel.module.css";

export interface TaskOutcomesPanelProps {
  outcomes: TaskOutcomes;
}

const STATUS_LABEL: Record<TaskStatus, string> = {
  ready: "Ready",
  in_progress: "In progress",
  blocked: "Blocked",
  needs_review: "Needs review",
  done: "Done",
  wont_do: "Won't do",
};

// Status is a severity encoding, not a categorical identity — reuse the
// semantic tokens, not --chart-*, per HANDOFF.md §6. Only 4 semantic tokens
// exist for 6 statuses, so info/warn are each shared by two statuses; every
// segment/legend entry also carries a text label, so identity never rests on
// color alone.
const STATUS_COLOR: Record<TaskStatus, string> = {
  done: "var(--ok)",
  in_progress: "var(--info)",
  ready: "var(--info)",
  needs_review: "var(--warn)",
  wont_do: "var(--warn)",
  blocked: "var(--bad)",
};

const STATUS_ORDER: TaskStatus[] = ["done", "in_progress", "ready", "needs_review", "wont_do", "blocked"];

/** Task status distribution bar (HANDOFF.md §6): semantic status tokens, legend, table toggle. */
export function TaskOutcomesPanel({ outcomes }: TaskOutcomesPanelProps) {
  const [showTable, setShowTable] = useState(false);
  const entries = STATUS_ORDER.map((status) => ({ status, count: outcomes.counts[status] ?? 0 })).filter(
    (e) => e.count > 0,
  );

  if (outcomes.total === 0) {
    return <div className={styles.empty}>No task activity yet.</div>;
  }

  return (
    <div className={styles.panel}>
      <div className={styles.header}>
        <div className={styles.legend}>
          {entries.map((e) => (
            <span key={e.status} className={styles.legendItem}>
              <span className={styles.swatch} style={{ background: STATUS_COLOR[e.status] }} />
              {STATUS_LABEL[e.status]} ({e.count})
            </span>
          ))}
        </div>
        <button
          type="button"
          className={styles.toggle}
          onClick={() => setShowTable((s) => !s)}
          aria-pressed={showTable}
        >
          {showTable ? "Show chart" : "Show table"}
        </button>
      </div>

      {showTable ? (
        <table className={styles.table}>
          <thead>
            <tr>
              <th>Status</th>
              <th>Count</th>
              <th>Share</th>
            </tr>
          </thead>
          <tbody>
            {entries.map((e) => (
              <tr key={e.status}>
                <td>{STATUS_LABEL[e.status]}</td>
                <td>{e.count}</td>
                <td>{Math.round((e.count / outcomes.total) * 100)}%</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <div className={styles.bar}>
          {entries.map((e) => (
            <div
              key={e.status}
              className={styles.segment}
              style={{ width: `${(e.count / outcomes.total) * 100}%`, background: STATUS_COLOR[e.status] }}
              title={`${STATUS_LABEL[e.status]}: ${e.count}`}
            />
          ))}
        </div>
      )}
      <div className={styles.summary}>
        {outcomes.total} tasks · {Math.round(outcomes.completion_rate * 100)}% completion rate
      </div>
    </div>
  );
}
