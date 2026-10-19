import { useEffect, useMemo, useState } from "react";
import { Button } from "../../components/Button/Button";
import { Chip, type ChipTone } from "../../components/Chip/Chip";
import { SegmentedControl } from "../../components/SegmentedControl/SegmentedControl";
import { Select } from "../../components/Select/Select";
import type { PipelineClient, TaskStreamTarget } from "../../pipeline/client/pipelineClient";
import { AgentOutputView } from "../../pipeline/DrillIn/AgentOutputView";
import { DrillInFeed } from "../../pipeline/DrillIn/DrillInFeed";
import { StageTimeline } from "../../pipeline/DrillIn/StageTimeline";
import { TaskFiles } from "../../pipeline/DrillIn/TaskFiles";
import type { RunStage } from "../../pipeline/plan/runStages";
import type { RunStatus } from "../../pipeline/plan/runStatus";
import type { StageAttempt, TaskRunState } from "../../pipeline/state/usePipelineRun";
import { useEscapeLayer } from "../../platform/escapeLayers";
import type { Task } from "../../router/types";
import { usePrefersReducedMotion } from "../GraphView/usePrefersReducedMotion";
import styles from "./TaskLens.module.css";

export type TaskDetailsMode = "details" | "retry";

export interface TaskLensProps {
  task: Task;
  tasksById: Record<string, Task>;
  /** Where the task stands in the run being viewed (runStatusOf). */
  runStatus: RunStatus;
  /** The run's stages, in order (runStages). */
  stages: RunStage[];
  /** The task's fold for the run being viewed. Absent: nothing ran yet. */
  runState?: TaskRunState;
  sessionId: string;
  pipelineClient: PipelineClient;
  /** run_seq of the run being viewed; passed to every stream target. */
  runSeq?: number;
  /** The viewed run is the live one (a started attempt is still being written). */
  live: boolean;
  onClose: () => void;
  onOpenDetails: (taskId: string, mode: TaskDetailsMode) => void;
  /** Opens a touched file in the Changes tab. Rows aren't clickable without it. */
  onOpenFile?: (path: string) => void;
  onRetry: (taskId: string) => void;
  onWontDo: (taskId: string) => void;
  /** Hidden when absent. */
  onDiscuss?: (taskId: string) => void;
  /** Overrides the last failed attempt's reason in the blocked box. */
  blockReason?: string;
  className?: string;
}

const STATUS_CHIP: Record<RunStatus, { label: string; tone: ChipTone }> = {
  ready: { label: "ready", tone: "neutral" },
  queued: { label: "queued", tone: "neutral" },
  running: { label: "running", tone: "info" },
  blocked: { label: "blocked", tone: "bad" },
  done: { label: "done", tone: "ok" },
  skipped: { label: "not in this run", tone: "neutral" },
  excluded: { label: "not in this run", tone: "neutral" },
  draft: { label: "draft", tone: "draft" },
};

type StationState = "wait" | "now" | "done" | "fail" | "skip";

const STATION_STATE: Record<StageAttempt["status"], StationState> = {
  started: "now",
  completed: "done",
  failed: "fail",
  skipped: "skip",
};

const STATION_LABEL: Record<StationState, string> = {
  wait: "not reached",
  now: "running",
  done: "done",
  fail: "failed",
  skip: "skipped",
};

function plural(n: number, one: string, many = `${one}s`) {
  return `${n} ${n === 1 ? one : many}`;
}

/**
 * One task's journey through a run, shared by the List and Graph views
 * (docs/TASKS-PIPELINE-DESIGN.md): the stage strip, every attempt, the
 * selected attempt's output, files touched and acceptance criteria. Esc
 * closes it (a detail sheet opened over it takes Esc first).
 */
export function TaskLens({
  task,
  tasksById,
  runStatus,
  stages,
  runState,
  sessionId,
  pipelineClient,
  runSeq,
  live,
  onClose,
  onOpenDetails,
  onOpenFile,
  onRetry,
  onWontDo,
  onDiscuss,
  blockReason,
  className,
}: TaskLensProps) {
  useEscapeLayer(true, onClose);
  const reducedMotion = usePrefersReducedMotion();
  const attempts = useMemo(() => runState?.attempts ?? [], [runState]);

  // null follows the newest attempt, so a live task's next stage takes over
  // the output pane as it starts. Picking the newest attempt goes back to
  // following. Attempts only ever append within a run, so an index is stable.
  const [picked, setPicked] = useState<number | null>(null);
  useEffect(() => setPicked(null), [task.id, runSeq]);
  const selectedIndex = picked !== null && picked < attempts.length ? picked : attempts.length - 1;
  const selected: StageAttempt | undefined = attempts[selectedIndex];
  function select(index: number) {
    setPicked(index >= attempts.length - 1 ? null : index);
  }

  const stageByName = useMemo(() => new Map(stages.map((s) => [s.name, s])), [stages]);
  // A stage the run names but the template doesn't still gets a station.
  const stripStages = useMemo(() => {
    const names = new Set(stages.map((s) => s.name));
    const extra: RunStage[] = [];
    for (const a of attempts) {
      if (!names.has(a.stage)) {
        names.add(a.stage);
        extra.push({ name: a.stage, kind: "agent" });
      }
    }
    return [...stages, ...extra];
  }, [stages, attempts]);

  const chip = STATUS_CHIP[runStatus];

  return (
    <section
      className={[styles.lens, className].filter(Boolean).join(" ")}
      aria-label={`Task: ${task.title}`}
      data-testid="task-lens"
    >
      <header className={styles.head}>
        <RunGlyph status={runStatus} animate={!reducedMotion} />
        <span className={styles.title}>{task.title}</span>
        <Chip tone={chip.tone}>{chip.label}</Chip>
        {(task.labels ?? []).map((label) => (
          <Chip key={label}>{label}</Chip>
        ))}
        <span className={styles.spacer} />
        <Button variant="ghost" size="sm" onClick={() => onOpenDetails(task.id, "details")}>
          Task details
        </Button>
        <button type="button" className={styles.close} onClick={onClose} aria-label="Close">
          <kbd className={styles.kbd}>Esc</kbd> close
        </button>
      </header>

      <ol className={styles.strip} aria-label="Stages">
        {stripStages.map((stage, i) => {
          const own = attempts.filter((a) => a.stage === stage.name);
          const latest = own[own.length - 1];
          const state: StationState = latest ? STATION_STATE[latest.status] : "wait";
          const lastIndex = latest ? attempts.lastIndexOf(latest) : -1;
          const sub =
            own.length > 1
              ? plural(own.length, "attempt")
              : state === "skip"
                ? stage.when
                  ? "skipped (when:)"
                  : "skipped"
                : stage.kind === "command"
                  ? "command"
                  : "";
          return (
            <li key={stage.name} className={styles.stationItem}>
              {i > 0 && (
                <span
                  className={[styles.wire, state !== "wait" && state !== "skip" && styles.wireOn]
                    .filter(Boolean)
                    .join(" ")}
                  aria-hidden="true"
                />
              )}
              <button
                type="button"
                className={[styles.station, styles[state], selected?.stage === stage.name && styles.sel]
                  .filter(Boolean)
                  .join(" ")}
                data-state={state}
                data-still={reducedMotion || undefined}
                disabled={!latest}
                aria-pressed={selected?.stage === stage.name}
                aria-label={`${stage.name}: ${STATION_LABEL[state]}${own.length > 1 ? `, ${own.length} attempts` : ""}`}
                onClick={() => select(lastIndex)}
              >
                <span className={styles.dot} aria-hidden="true">
                  {state === "done" ? "✓" : state === "fail" ? "✕" : ""}
                </span>
                <span className={styles.stationName}>{stage.name}</span>
                <span className={[styles.stationSub, own.length > 1 && styles.warn].filter(Boolean).join(" ")}>
                  {sub}
                </span>
              </button>
            </li>
          );
        })}
      </ol>

      {runStatus === "blocked" && (
        <BlockedBox
          task={task}
          attempts={attempts}
          runState={runState}
          stageByName={stageByName}
          blockReason={blockReason}
          onOpenDetails={onOpenDetails}
          onRetry={onRetry}
          onWontDo={onWontDo}
          onDiscuss={onDiscuss}
        />
      )}

      <div className={styles.body}>
        <div className={styles.side}>
          <div>
            <h4 className={styles.h}>Timeline</h4>
            {attempts.length === 0 ? (
              <p className={styles.muted}>Not started.</p>
            ) : (
              <StageTimeline attempts={attempts} selectedIndex={selectedIndex} onSelect={select} />
            )}
          </div>
          <div>
            <h4 className={styles.h}>Files touched</h4>
            {(runState?.touchedFiles.length ?? 0) === 0 ? (
              <p className={styles.muted}>None yet.</p>
            ) : (
              <TaskFiles touchedFiles={runState?.touchedFiles ?? []} onOpenFile={onOpenFile} showCount={false} />
            )}
          </div>
          <div>
            <h4 className={styles.h}>Acceptance</h4>
            {task.acceptance_criteria ? (
              <p className={styles.acceptance}>{task.acceptance_criteria}</p>
            ) : (
              <p className={styles.muted}>None written.</p>
            )}
          </div>
        </div>
        <div className={styles.out}>
          {selected ? (
            <AttemptOutput
              key={`${task.id}|${runSeq ?? ""}`}
              task={task}
              attempts={attempts}
              index={selectedIndex}
              stage={stageByName.get(selected.stage)}
              sessionId={sessionId}
              pipelineClient={pipelineClient}
              runSeq={runSeq}
              live={live}
              onSelect={select}
            />
          ) : (
            <p className={styles.emptyOut}>{emptyOutputText(task, runStatus, tasksById)}</p>
          )}
        </div>
      </div>
    </section>
  );
}

function emptyOutputText(task: Task, status: RunStatus, tasksById: Record<string, Task>): string {
  switch (status) {
    case "queued": {
      const waits = (task.dependencies ?? [])
        .map((id) => tasksById[id])
        .filter((t): t is Task => !!t && t.status !== "done")
        .map((t) => t.title);
      return waits.length > 0
        ? `Queued: waits on ${waits.join(", ")}. Nothing to show until a stage starts.`
        : "Queued. Nothing to show until a stage starts.";
    }
    case "ready":
      return "Ready. Nothing to show until the run starts.";
    case "draft":
      return "A draft. It runs once it's confirmed.";
    case "skipped":
    case "excluded":
      return "This task isn't in the run.";
    default:
      return "No stage ran for this task in this run.";
  }
}

function RunGlyph({ status, animate }: { status: RunStatus; animate: boolean }) {
  return (
    <span
      aria-hidden="true"
      className={[styles.glyph, styles[`g_${status}`], status === "running" && animate && styles.spin]
        .filter(Boolean)
        .join(" ")}
    />
  );
}

interface BlockedBoxProps {
  task: Task;
  attempts: StageAttempt[];
  runState?: TaskRunState;
  stageByName: Map<string, RunStage>;
  blockReason?: string;
  onOpenDetails: (taskId: string, mode: TaskDetailsMode) => void;
  onRetry: (taskId: string) => void;
  onWontDo: (taskId: string) => void;
  onDiscuss?: (taskId: string) => void;
}

function BlockedBox({
  task,
  attempts,
  runState,
  stageByName,
  blockReason,
  onOpenDetails,
  onRetry,
  onWontDo,
  onDiscuss,
}: BlockedBoxProps) {
  const lastFailed = [...attempts].reverse().find((a) => a.status === "failed");
  const at = lastFailed?.stage ?? runState?.currentStage;
  // Every failure of the blocking stage but the last sent the task back once.
  const loopBacks = at
    ? Math.max(0, attempts.filter((a) => a.stage === at && a.status === "failed").length - 1)
    : 0;
  const then = at ? stageByName.get(at)?.then : undefined;
  const reason = blockReason ?? lastFailed?.reason;
  return (
    <div className={styles.blocked} role="group" aria-label="Blocked">
      <p className={styles.blockedText}>
        <b>{at ? `Blocked at ${at}` : "Blocked"}</b>
        {loopBacks > 0 && ` after ${plural(loopBacks, "loop-back")}`}
        {then && (
          <>
            {" "}
            (<code>then: {then}</code>)
          </>
        )}
        .{reason && ` ${reason}`}
      </p>
      <div className={styles.blockedActions}>
        <Button variant="primary" size="sm" onClick={() => onOpenDetails(task.id, "retry")}>
          Edit &amp; retry
        </Button>
        <Button size="sm" onClick={() => onRetry(task.id)}>
          Retry as is
        </Button>
        {onDiscuss && (
          <Button size="sm" onClick={() => onDiscuss(task.id)}>
            Discuss in Conversation
          </Button>
        )}
        <Button variant="ghost" size="sm" onClick={() => onWontDo(task.id)}>
          Won&apos;t do
        </Button>
      </div>
    </div>
  );
}

type OutputView = "turns" | "raw";

const VIEW_OPTIONS = [
  { value: "turns" as const, label: "Turns" },
  { value: "raw" as const, label: "Raw log" },
];

interface AttemptOutputProps {
  task: Task;
  attempts: StageAttempt[];
  index: number;
  stage?: RunStage;
  sessionId: string;
  pipelineClient: PipelineClient;
  runSeq?: number;
  live: boolean;
  onSelect: (index: number) => void;
}

function AttemptOutput({
  task,
  attempts,
  index,
  stage,
  sessionId,
  pipelineClient,
  runSeq,
  live,
  onSelect,
}: AttemptOutputProps) {
  const attempt = attempts[index];
  const [view, setView] = useState<OutputView>("turns");
  const isCommand = stage?.kind === "command";
  const raw = isCommand || view === "raw";
  const isLive = live && attempt.status === "started";
  const streams = attempt.status !== "skipped";

  const same = attempts.map((a, i) => ({ a, i })).filter(({ a }) => a.stage === attempt.stage);

  // Opening an EventSource is a side effect: once per selected attempt (and
  // view: the raw view tears its source down when it unmounts), never once
  // per render, and closed on change and unmount. Same idiom as PipelineTab.
  const streamKey = streams ? `${runSeq ?? ""}|${attempt.stage}|${attempt.attempt}|${raw ? "raw" : "turns"}` : null;
  const target: TaskStreamTarget = { run: runSeq, stage: attempt.stage, attempt: attempt.attempt };
  const outputSource = useMemo(
    () => (streamKey ? pipelineClient.streamTaskOutput(sessionId, task.id, target) : undefined),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [pipelineClient, sessionId, task.id, streamKey],
  );
  useEffect(() => () => outputSource?.close(), [outputSource]);
  const turnsSource = useMemo(
    () => (streamKey && !raw ? pipelineClient.streamTaskTurns(sessionId, task.id, target) : undefined),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [pipelineClient, sessionId, task.id, streamKey],
  );
  useEffect(() => () => turnsSource?.close(), [turnsSource]);

  // An attempt a loop-back started carries the failure that sent it here.
  const prev = attempts[index - 1];
  const handoffFrom = attempt.attempt > 1 && prev?.status === "failed" && prev.reason ? prev : undefined;
  const next = attempts[index + 1];
  const handoffTo = attempt.status === "failed" && next && next.attempt > 1 ? next : undefined;

  return (
    <>
      <div className={styles.outHead}>
        <b className={styles.outStage}>{attempt.stage}</b>
        {same.length > 1 ? (
          <Select
            className={styles.attemptSelect}
            value={String(index)}
            onChange={(v) => onSelect(Number(v))}
            options={same.map(({ a, i }) => ({
              value: String(i),
              label: `attempt ${a.attempt} of ${same.length}${
                a.status === "started" && live ? " · live" : a.status === "failed" ? " · failed" : ""
              }`,
            }))}
          />
        ) : (
          attempt.attempt > 1 && <span>attempt {attempt.attempt}</span>
        )}
        {isLive ? (
          <Chip tone="info">live</Chip>
        ) : (
          <Chip tone={attempt.status === "failed" ? "bad" : attempt.status === "completed" ? "ok" : "neutral"}>
            {attempt.status}
          </Chip>
        )}
        {!!attempt.retries && <Chip tone="warn">retried ×{attempt.retries}</Chip>}
        {isCommand && <span>command</span>}
        <span className={styles.spacer} />
        {!isCommand && streams && (
          <SegmentedControl
            aria-label="Output view"
            className={styles.viewToggle}
            value={view}
            options={VIEW_OPTIONS}
            onChange={setView}
          />
        )}
      </div>
      {handoffFrom && (
        <p className={styles.handoff} data-testid="handoff">
          ↳ Started from{" "}
          <b>
            {handoffFrom.stage} attempt {handoffFrom.attempt}
          </b>{" "}
          feedback: “{handoffFrom.reason}”
        </p>
      )}
      {handoffTo && (
        <p className={styles.handoff}>
          ↳ Passed to{" "}
          <b>
            {handoffTo.stage} attempt {handoffTo.attempt}
          </b>{" "}
          as its feedback.
        </p>
      )}
      <div className={styles.feed}>
        {!streams ? (
          <p className={styles.emptyOut}>
            {stage?.when ? (
              <>
                Skipped: <code>when: {stage.when}</code> was false for this task.
              </>
            ) : (
              "Skipped for this task."
            )}
          </p>
        ) : raw || !turnsSource ? (
          outputSource && <AgentOutputView key={streamKey} source={outputSource} />
        ) : (
          outputSource && (
            <DrillInFeed
              key={streamKey}
              task={task}
              attempts={[attempt]}
              outputSource={outputSource}
              turnsSource={turnsSource}
              showHeader={false}
            />
          )
        )}
      </div>
    </>
  );
}
