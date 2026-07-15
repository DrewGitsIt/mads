import { useState } from "react";
import { RunHeader } from "./RunHeader/RunHeader";
import { StageBoard } from "./Board/StageBoard";
import { BlockedPanel } from "./Board/BlockedPanel";
import { TaskDrillIn } from "./DrillIn/TaskDrillIn";
import { usePipelineRun } from "./state/usePipelineRun";
import type { PipelineClient } from "./client/pipelineClient";
import styles from "./PipelineTab.module.css";

export interface PipelineTabProps {
  sessionId: string;
  pipelineClient: PipelineClient;
}

/** Top-level Pipeline tab: composes the run header, stage board, blocked-tasks panel, and drill-in. */
export function PipelineTab({ sessionId, pipelineClient }: PipelineTabProps) {
  const { tasks, phase, outcome, runStartedAt, runEndedAt, columns, taskRunState } = usePipelineRun(
    sessionId,
    pipelineClient,
  );
  const [runId, setRunId] = useState<string | undefined>();
  const [selectedTaskId, setSelectedTaskId] = useState<string | undefined>();

  const selectedTask = tasks.find((t) => t.id === selectedTaskId);
  const selectedRunState = selectedTaskId ? taskRunState[selectedTaskId] : undefined;

  return (
    <div className={styles.tab}>
      <RunHeader
        sessionId={sessionId}
        client={pipelineClient}
        phase={phase}
        outcome={outcome}
        runStartedAt={runStartedAt}
        runEndedAt={runEndedAt}
        tasks={tasks}
        onRunStarted={setRunId}
      />
      <BlockedPanel
        tasks={tasks}
        taskRunState={taskRunState}
        client={pipelineClient}
        onSelectTask={setSelectedTaskId}
      />
      <StageBoard
        columns={columns}
        tasks={tasks}
        taskRunState={taskRunState}
        onSelectTask={setSelectedTaskId}
      />
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
