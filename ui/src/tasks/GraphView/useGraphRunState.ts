import type { PipelineClient } from "../../pipeline/client/pipelineClient";
import { useResumingRunFold, type RunFoldState } from "../../pipeline/state/usePipelineRun";
import type { PipelineRun } from "../../router/types";

// Newest run by run_seq, regardless of outcome — a blocked task's run is
// usually already terminal (the engine keeps processing other tasks after
// one blocks, and only emits pipeline.completed once the whole run goes
// quiescent), so restricting to outcome === "running" like usePipelineRun
// does would miss the common "review a blocked task after its run already
// finished" case. Assumes at most one non-terminal run per session.
function pickNewestRun(runs: PipelineRun[]): PipelineRun | undefined {
  if (runs.length === 0) return undefined;
  return [...runs].sort((a, b) => b.run_seq - a.run_seq)[0];
}

/**
 * Opt-in mirror of usePipelineRun's fold logic without its duplicate task
 * fetch — GraphView already has tasks from useTaskStore, no refetch needed.
 * Returns undefined when client is absent so callers can render without
 * ever constructing an EventSource. Shares its resume-then-subscribe
 * mechanics with usePipelineRun via useResumingRunFold (HANDOFF.md) so a
 * fix to one can't silently miss the other.
 */
export function useGraphRunState(
  sessionId: string,
  client: PipelineClient | undefined,
): RunFoldState | undefined {
  const { state } = useResumingRunFold(sessionId, client, pickNewestRun);
  return client ? state : undefined;
}
