import { useState } from "react";
import { Popover } from "../../components/Popover/Popover";
import type { PipelineRun } from "../../router/types";
import styles from "./RunPicker.module.css";

export interface RunPickerProps {
  /** Newest-first. */
  runs: PipelineRun[];
  selectedRunSeq?: number;
  onSelect: (runSeq: number) => void;
}

function startOfDay(d: Date): number {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
}

function relativeDay(d: Date): string {
  const days = Math.floor((startOfDay(new Date()) - startOfDay(d)) / 86_400_000);
  if (days === 0) return "today";
  if (days === 1) return "yesterday";
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

function formatRunLabel(run: PipelineRun): string {
  const started = new Date(run.started_at);
  const when = `${relativeDay(started)}, ${started.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" })}`;
  const outcome = run.outcome === "running" ? "in progress" : run.outcome;
  return `Run · started ${when} · ${outcome} · ${run.tasks_done} done / ${run.tasks_blocked} blocked`;
}

/** Run history picker for the Pipeline tab, built on the kit's Popover-trigger+menu-of-options idiom (HANDOFF.md §4). */
export function RunPicker({ runs, selectedRunSeq, onSelect }: RunPickerProps) {
  const [open, setOpen] = useState(false);
  const selected = runs.find((r) => r.run_seq === selectedRunSeq);

  return (
    <Popover
      open={open}
      onClose={() => setOpen(false)}
      anchor={
        <button
          type="button"
          className={styles.trigger}
          aria-label="Run history"
          onClick={() => setOpen((o) => !o)}
        >
          {selected ? formatRunLabel(selected) : "Select a run"}
        </button>
      }
    >
      <div className={styles.menu}>
        {runs.map((run) => (
          <button
            key={run.run_seq}
            type="button"
            className={[styles.option, run.run_seq === selectedRunSeq ? styles.optionSelected : ""]
              .filter(Boolean)
              .join(" ")}
            onClick={() => {
              onSelect(run.run_seq);
              setOpen(false);
            }}
          >
            {formatRunLabel(run)}
          </button>
        ))}
      </div>
    </Popover>
  );
}
