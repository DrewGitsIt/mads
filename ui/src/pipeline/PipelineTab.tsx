import { useEffect, useMemo, useState } from "react";
import { RunHeader } from "./RunHeader/RunHeader";
import { StageBoard } from "./Board/StageBoard";
import { BlockedPanel } from "./Board/BlockedPanel";
import { TaskDrillIn } from "./DrillIn/TaskDrillIn";
import { RunPicker } from "./RunPicker/RunPicker";
import { usePipelineRun } from "./state/usePipelineRun";
import { usePipelineRunHistory } from "./state/usePipelineRunHistory";
import type { PipelineClient } from "./client/pipelineClient";
import styles from "./PipelineTab.module.css";

export interface PipelineTabProps {
  sessionId: string;
  pipelineClient: PipelineClient;
}

/** Top-level Pipeline tab: composes the run header, run picker, stage board, blocked-tasks panel, and drill-in. */
export function PipelineTab({ sessionId, pipelineClient }: PipelineTabProps) {
  const live = usePipelineRun(sessionId, pipelineClient);
  const [selectedTaskId, setSelectedTaskId] = useState<string | undefined>();

  // Once a live run has ever started this mount, it preempts the historical
  // view for the rest of the mount, even after it finishes (HANDOFF.md §4e).
  const liveStarted = live.phase !== "idle";
  const history = usePipelineRunHistory(sessionId, pipelineClient, !liveStarted);
  const selectedRun = history.runs.find((r) => r.run_seq === history.selectedRunSeq);

  const showHistoryPicker = !liveStarted && history.runs.length > 0;
  const showEmptyState = !liveStarted && !history.loading && history.runs.length === 0;

  // RunHeader gets the live fold's own phase once a live run has started;
  // otherwise it gets the selected run's fields, never the raw historical
  // fold's phase (which would read "running" mid-fold) — "finished" only
  // once a run is actually selected, so a genuinely run-less session still
  // shows "idle" instead of a misleading "finished" (HANDOFF.md §4e).
  const headerPhase = liveStarted ? live.phase : selectedRun ? "finished" : "idle";
  const headerOutcome = liveStarted ? live.outcome : selectedRun?.outcome;
  const headerStartedAt = liveStarted ? live.runStartedAt : selectedRun?.started_at;
  const headerEndedAt = liveStarted ? live.runEndedAt : selectedRun?.ended_at;

  const columns = liveStarted ? live.columns : history.columns;
  const taskRunState = liveStarted ? live.taskRunState : history.taskRunState;

  const selectedTask = live.tasks.find((t) => t.id === selectedTaskId);
  const selectedRunState = selectedTaskId ? taskRunState[selectedTaskId] : undefined;

  // Opening an EventSource is a side effect — it must happen once per task
  // selection, not once per re-render (PipelineTab re-renders on every live
  // polling tick), and the connection must be explicitly closed on switch/unmount.
  const outputSource = useMemo(
    () => (selectedTask ? pipelineClient.streamTaskOutput(sessionId, selectedTask.id) : undefined),
    // selectedTask?.id (not selectedTask) is deliberate — live.tasks is a new
    // array each poll, so the object identity changes every tick even when
    // the selected id doesn't, which would otherwise reopen the stream.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [pipelineClient, sessionId, selectedTask?.id],
  );
  useEffect(() => {
    return () => outputSource?.close();
  }, [outputSource]);

  return (
    <div className={styles.tab}>
      <RunHeader
        sessionId={sessionId}
        client={pipelineClient}
        phase={headerPhase}
        outcome={headerOutcome}
        runStartedAt={headerStartedAt}
        runEndedAt={headerEndedAt}
        tasks={live.tasks}
      />
      {showHistoryPicker && (
        <RunPicker runs={history.runs} selectedRunSeq={history.selectedRunSeq} onSelect={history.selectRun} />
      )}
      {showEmptyState ? (
        <div className={styles.empty}>
          <p className={styles.emptyTitle}>No pipeline runs yet</p>
        </div>
      ) : (
        <>
          <BlockedPanel
            tasks={live.tasks}
            taskRunState={taskRunState}
            client={pipelineClient}
            onSelectTask={setSelectedTaskId}
          />
          <StageBoard
            columns={columns}
            tasks={live.tasks}
            taskRunState={taskRunState}
            onSelectTask={setSelectedTaskId}
          />
        </>
      )}
      {selectedTask && outputSource && (
        <TaskDrillIn
          task={selectedTask}
          attempts={selectedRunState?.attempts ?? []}
          touchedFiles={selectedRunState?.touchedFiles ?? []}
          outputSource={outputSource}
          onClose={() => setSelectedTaskId(undefined)}
        />
      )}
    </div>
  );
}
