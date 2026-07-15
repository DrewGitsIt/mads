import { Button } from "../../components/Button/Button";
import { Chip, type ChipTone } from "../../components/Chip/Chip";
import type { PipelineRun, PipelineRunOutcome } from "../../router/types";
import styles from "./RunHistoryTable.module.css";

export interface RunHistoryTableProps {
  runs: PipelineRun[];
  /**
   * There's no cross-surface router in this codebase yet (HANDOFF.md §7) —
   * this callback is the whole of "drill-back is wired." Opening the
   * session's Pipeline tab (e.g. navigating pipeline.html?session=...) is
   * the caller's decision, deliberately left out of this module.
   */
  onDrillBack?: (params: { sessionId: string; runSeq: number }) => void;
}

function outcomeTone(outcome: PipelineRunOutcome): ChipTone {
  switch (outcome) {
    case "completed":
      return "ok";
    case "failed":
      return "bad";
    case "incomplete":
    case "cancelled":
      return "warn";
    case "running":
      return "info";
    default:
      return "neutral";
  }
}

/** Pipeline run history table with a per-row drill-back action (HANDOFF.md §7). */
export function RunHistoryTable({ runs, onDrillBack }: RunHistoryTableProps) {
  if (runs.length === 0) {
    return <div className={styles.empty}>No pipeline runs recorded yet.</div>;
  }

  return (
    <table className={styles.table}>
      <thead>
        <tr>
          <th>Session</th>
          <th>Run</th>
          <th>Started</th>
          <th>Outcome</th>
          <th>Tasks (done/blocked/skipped)</th>
          <th>Cost</th>
          <th>Agents</th>
          <th />
        </tr>
      </thead>
      <tbody>
        {runs.map((run) => (
          <tr key={`${run.session_id}-${run.run_seq}`}>
            <td>{run.session_id}</td>
            <td>{run.run_seq}</td>
            <td>{run.started_at}</td>
            <td>
              <Chip tone={outcomeTone(run.outcome)}>{run.outcome}</Chip>
            </td>
            <td>
              {run.tasks_done}/{run.tasks_blocked}/{run.tasks_skipped}
            </td>
            <td>${run.cost_usd.toFixed(2)}</td>
            <td>{(run.agents ?? []).join(", ")}</td>
            <td>
              {onDrillBack && (
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => onDrillBack({ sessionId: run.session_id, runSeq: run.run_seq })}
                >
                  View run
                </Button>
              )}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
