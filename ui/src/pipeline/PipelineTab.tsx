import { useState } from "react";
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
  const [runId, setRunId] = useState<string | undefined>();
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
        onRunStarted={setRunId}
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
      {selectedTask && (
        <TaskDrillIn
          task={selectedTask}
          attempts={selectedRunState?.attempts ?? []}
          touchedFiles={selectedRunState?.touchedFiles ?? []}
          outputSource={pipelineClient.streamTaskOutput(runId ?? "", selectedTask.id)}
          onClose={() => setSelectedTaskId(undefined)}
        />
      )}
    </div>
  );
}
