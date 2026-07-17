import type { PipelineRun } from "../../router/types";
import styles from "./RunPicker.module.css";

export interface RunPickerProps {
  /** Newest-first. */
  runs: PipelineRun[];
  selectedRunSeq?: number;
  onSelect: (runSeq: number) => void;
}

function formatStatusLine(run: PipelineRun): string {
  if (run.outcome === "running") {
    return `Run started ${new Date(run.started_at).toLocaleString()}`;
  }
  const ended = run.ended_at ? new Date(run.ended_at).toLocaleString() : "";
  return `Run ${run.outcome} ${ended} · done ${run.tasks_done} · blocked ${run.tasks_blocked}`;
}

/** Simple newest-first run picker for the Pipeline tab's historical view (HANDOFF.md §4d). */
export function RunPicker({ runs, selectedRunSeq, onSelect }: RunPickerProps) {
  const selected = runs.find((r) => r.run_seq === selectedRunSeq);

  return (
    <div className={styles.picker}>
      <select
        className={styles.select}
        aria-label="Pipeline run"
        value={selectedRunSeq ?? ""}
        onChange={(e) => onSelect(Number(e.target.value))}
      >
        {runs.map((run) => (
          <option key={run.run_seq} value={run.run_seq}>
            {new Date(run.started_at).toLocaleString()} — {run.outcome}
          </option>
        ))}
      </select>
      {selected && <span className={styles.status}>{formatStatusLine(selected)}</span>}
    </div>
  );
}
