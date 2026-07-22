import { useEffect, useMemo, useRef, useState } from "react";
import { RunHeader } from "./RunHeader/RunHeader";
import { StageBoard } from "./Board/StageBoard";
import { BlockedPanel } from "./Board/BlockedPanel";
import { TaskDrillIn } from "./DrillIn/TaskDrillIn";
import { RunPicker } from "./RunPicker/RunPicker";
import { usePipelineRun, type UsePipelineRunResult } from "./state/usePipelineRun";
import { usePipelineRunHistory } from "./state/usePipelineRunHistory";
import type { PipelineClient } from "./client/pipelineClient";
import type { PipelineRun, PipelineRunOutcome } from "../router/types";
import styles from "./PipelineTab.module.css";

export interface PipelineTabProps {
  sessionId: string;
  pipelineClient: PipelineClient;
}

/** Synthesizes a PipelineRun-shaped entry for RunPicker from the live fold, so the in-flight run can be shown/labeled while it's running (HANDOFF.md §3). */
function liveRunEntry(sessionId: string, live: UsePipelineRunResult): PipelineRun | undefined {
  if (live.phase === "idle" || live.runStartSeq === undefined) return undefined;
  let done = 0;
  let blocked = 0;
  for (const t of live.tasks) {
    if (t.status === "done") done++;
    else if (t.status === "blocked") blocked++;
  }
  return {
    session_id: sessionId,
    run_seq: live.runStartSeq,
    started_at: live.runStartedAt ?? "",
    ended_at: live.runEndedAt,
    outcome: (live.phase === "running" ? "running" : (live.outcome ?? "completed")) as PipelineRunOutcome,
    tasks_done: done,
    tasks_blocked: blocked,
    tasks_skipped: 0,
    cost_usd: 0,
  };
}

/** Top-level Pipeline tab: composes the run header, run picker, stage board, blocked-tasks panel, and drill-in. */
export function PipelineTab({ sessionId, pipelineClient }: PipelineTabProps) {
  const live = usePipelineRun(sessionId, pipelineClient);
  const [selectedTaskId, setSelectedTaskId] = useState<string | undefined>();
  const tabRef = useRef<HTMLDivElement>(null);
  const [tabHeight, setTabHeight] = useState(0);

  useEffect(() => {
    const el = tabRef.current;
    if (!el) return;
    const observer = new ResizeObserver(([entry]) => {
      setTabHeight(entry.contentRect.height);
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  // Once a live run has ever started this mount, it preempts the historical
  // view for the rest of the mount, even after it finishes (HANDOFF.md §4e).
  const liveStarted = live.phase !== "idle";
  // Also gated on !live.resuming: without this there's a one-tick flash where
  // liveStarted is still false (the resumed fold hasn't been seeded yet) and
  // the history hook briefly fetches/shows the old finished run before the
  // seed lands (HANDOFF.md §3).
  const history = usePipelineRunHistory(sessionId, pipelineClient, !liveStarted && !live.resuming);
  const selectedRun = history.runs.find((r) => r.run_seq === history.selectedRunSeq);

  const liveEntry = liveStarted ? liveRunEntry(sessionId, live) : undefined;
  const pickerRuns = liveEntry ? [liveEntry] : history.runs;
  const showPicker = pickerRuns.length > 0;
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
  // A historical/interrupted run's chip counts must describe that run, not
  // live.tasks (the session's current, unscoped task list) — otherwise a
  // stale run's header shows the session's lifetime totals (HANDOFF.md).
  const headerRunCounts =
    !liveStarted && selectedRun
      ? {
          total: selectedRun.tasks_done + selectedRun.tasks_blocked + selectedRun.tasks_skipped,
          inProgress: 0,
          done: selectedRun.tasks_done,
          blocked: selectedRun.tasks_blocked + selectedRun.tasks_skipped,
        }
      : undefined;

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

  const turnsSource = useMemo(
    () => (selectedTask ? pipelineClient.streamTaskTurns(sessionId, selectedTask.id) : undefined),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [pipelineClient, sessionId, selectedTask?.id],
  );
  useEffect(() => {
    return () => turnsSource?.close();
  }, [turnsSource]);

  return (
    <div className={styles.tab} ref={tabRef}>
      <RunHeader
        sessionId={sessionId}
        client={pipelineClient}
        phase={headerPhase}
        outcome={headerOutcome}
        runStartedAt={headerStartedAt}
        runEndedAt={headerEndedAt}
        tasks={live.tasks}
        runCounts={headerRunCounts}
      />
      {showPicker && (
        <RunPicker
          runs={pickerRuns}
          selectedRunSeq={liveEntry ? liveEntry.run_seq : history.selectedRunSeq}
          onSelect={liveEntry ? () => {} : history.selectRun}
        />
      )}
      {showEmptyState ? (
        <div className={styles.empty}>
          <p className={styles.emptyTitle}>No pipeline runs yet</p>
        </div>
      ) : (
        <div className={styles.boardArea}>
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
        </div>
      )}
      {selectedTask && outputSource && turnsSource && (
        <TaskDrillIn
          task={selectedTask}
          attempts={selectedRunState?.attempts ?? []}
          touchedFiles={selectedRunState?.touchedFiles ?? []}
          outputSource={outputSource}
          turnsSource={turnsSource}
          onClose={() => setSelectedTaskId(undefined)}
          tabHeight={tabHeight}
        />
      )}
    </div>
  );
}
