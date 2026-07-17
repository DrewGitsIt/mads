import { useCallback, useEffect, useState } from "react";
import type { PipelineRun } from "../../router/types";
import type { PipelineClient } from "../client/pipelineClient";
import { foldReducer, initialRunFoldState, type TaskRunState } from "./usePipelineRun";

export interface UsePipelineRunHistoryResult {
  /** Newest-first (sorted by run_seq desc). */
  runs: PipelineRun[];
  selectedRunSeq?: number;
  selectRun: (runSeq: number) => void;
  /** Folded board state for the selected run. */
  columns: string[];
  taskRunState: Record<string, TaskRunState>;
  loading: boolean;
  error: string | null;
}

/**
 * Fetches a session's pipeline run history (GET /metrics/pipelines) and
 * folds the selected run's own event window through the same
 * transport-agnostic fold usePipelineRun uses for live SSE (HANDOFF.md
 * §4b) — this is how the Pipeline tab renders a run that finished before
 * this mount ever opened a live connection (e.g. a CLI-driven run).
 *
 * `active` gates both fetches so this hook stays idle once a live run has
 * ever started this mount (PipelineTab.tsx composes the two, HANDOFF.md
 * §4e).
 */
export function usePipelineRunHistory(
  sessionId: string,
  client: PipelineClient,
  active: boolean,
): UsePipelineRunHistoryResult {
  const [runs, setRuns] = useState<PipelineRun[]>([]);
  const [selectedRunSeq, setSelectedRunSeq] = useState<number | undefined>(undefined);
  const [columns, setColumns] = useState<string[]>([]);
  const [taskRunState, setTaskRunState] = useState<Record<string, TaskRunState>>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!active) return;
    let cancelled = false;
    setLoading(true);
    client
      .listRuns(sessionId)
      .then((fetched) => {
        if (cancelled) return;
        const sorted = [...fetched].sort((a, b) => b.run_seq - a.run_seq);
        setRuns(sorted);
        // Default-select the newest terminal run, not a dangling "running"
        // one — unless every run is dangling, in which case fall back to
        // the overall newest. Never override a selection the caller already
        // made via selectRun.
        setSelectedRunSeq((current) => {
          if (current !== undefined) return current;
          const terminal = sorted.find((r) => r.outcome !== "running");
          return (terminal ?? sorted[0])?.run_seq;
        });
      })
      .catch((err) => {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [client, sessionId, active]);

  useEffect(() => {
    if (!active || selectedRunSeq === undefined) return;
    let cancelled = false;
    setLoading(true);
    // since is an exclusive lower bound (seq strictly greater than) — using
    // selectedRunSeq - 1 includes the run's own pipeline.started event,
    // which foldReducer needs to flip phase to "running" and unlock the
    // rest of the fold.
    const idx = runs.findIndex((r) => r.run_seq === selectedRunSeq);
    // The query has no upper bound over the wire; bound it client-side to
    // the next-newer run's own pipeline.started seq, or an older selection
    // would fold in that run's events too and foldReducer's pipeline.started
    // case would reset state down to the wrong (newer) run.
    const upperBound = idx > 0 ? runs[idx - 1].run_seq : undefined;
    client
      .getRunEvents(sessionId, selectedRunSeq - 1)
      .then((events) => {
        if (cancelled) return;
        const bounded =
          upperBound === undefined ? events : events.filter((env) => env.seq < upperBound);
        const folded = bounded.reduce(foldReducer, initialRunFoldState);
        setColumns(folded.columns);
        setTaskRunState(folded.taskRunState);
      })
      .catch((err) => {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [client, sessionId, active, selectedRunSeq, runs]);

  const selectRun = useCallback((runSeq: number) => {
    setSelectedRunSeq(runSeq);
  }, []);

  return { runs, selectedRunSeq, selectRun, columns, taskRunState, loading, error };
}
