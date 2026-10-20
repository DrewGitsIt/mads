import { useCallback, useEffect, useState } from "react";
import type { PipelineRun } from "../../router/types";
import type { PipelineClient } from "../client/pipelineClient";
import { foldReducer, initialRunFoldState, type TaskRunState } from "./usePipelineRun";

export interface UsePipelineRunHistoryResult {
  /** Newest-first (sorted by run_seq desc). */
  runs: PipelineRun[];
  selectedRunSeq?: number;
  selectRun: (runSeq: number) => void;
  /** The run the fold fields below describe; lags selectedRunSeq while a newly selected run loads. */
  foldedRunSeq?: number;
  /** Folded board state for the selected run. */
  columns: string[];
  taskRunState: Record<string, TaskRunState>;
  /** The selected run's pipeline.started `excluded_task_ids` (empty for older runs). */
  excludedTaskIds: string[];
  /** The selected run's pipeline.started `template` name. */
  template?: string;
  /** The run list has been fetched at least once (whatever the outcome). */
  loaded: boolean;
  loading: boolean;
  error: string | null;
}

/**
 * Fetches a session's pipeline run history (GET /metrics/pipelines) and
 * folds the selected run's own event window through the same
 * transport-agnostic fold usePipelineRun uses for live SSE (HANDOFF.md
 * §4b) — this is how the Tasks tab renders a run that finished before
 * this mount ever opened a live connection (e.g. a CLI-driven run).
 *
 * `active` gates both fetches. A change of `refreshKey` refetches the run
 * list (the Tasks tab passes the live run's phase, so a run that just
 * started or ended shows up in the run menu).
 */
export function usePipelineRunHistory(
  sessionId: string,
  client: PipelineClient,
  active: boolean,
  refreshKey?: string | number,
): UsePipelineRunHistoryResult {
  const [runs, setRuns] = useState<PipelineRun[]>([]);
  const [selectedRunSeq, setSelectedRunSeq] = useState<number | undefined>(undefined);
  const [columns, setColumns] = useState<string[]>([]);
  const [taskRunState, setTaskRunState] = useState<Record<string, TaskRunState>>({});
  const [excludedTaskIds, setExcludedTaskIds] = useState<string[]>([]);
  const [template, setTemplate] = useState<string | undefined>();
  const [foldedRunSeq, setFoldedRunSeq] = useState<number | undefined>();
  const [loaded, setLoaded] = useState(false);
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
        if (cancelled) return;
        setLoading(false);
        setLoaded(true);
      });
    return () => {
      cancelled = true;
    };
  }, [client, sessionId, active, refreshKey]);

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
        setExcludedTaskIds(folded.excludedTaskIds);
        setTemplate(folded.template);
        setFoldedRunSeq(selectedRunSeq);
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

  return { runs, selectedRunSeq, selectRun, foldedRunSeq, columns, taskRunState, excludedTaskIds, template, loaded, loading, error };
}
