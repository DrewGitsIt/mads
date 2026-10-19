import { useCallback, useEffect, useMemo, useState } from "react";
import type { PipelineClient } from "../pipeline/client/pipelineClient";
import { planWarning, runnableInPlan } from "../pipeline/plan/planScope";
import { runStages } from "../pipeline/plan/runStages";
import { interruptedIds, pickupLine, runStatusOf, type RunMode } from "../pipeline/plan/runStatus";
import { usePickup } from "../pipeline/plan/usePickup";
import { useRunTemplates } from "../pipeline/plan/useRunTemplates";
import { usePipelineRun, type TaskRunState, type UsePipelineRunResult } from "../pipeline/state/usePipelineRun";
import { usePipelineRunHistory } from "../pipeline/state/usePipelineRunHistory";
import { requestOpenLibrary } from "../appNavigation";
import type { RouterClient } from "../router/client";
import type { PipelineRun, PipelineRunOutcome, Task, Template } from "../router/types";
import { ContentColumn } from "../components/ContentColumn/ContentColumn";
import { Toast } from "../components/Toast/Toast";
import { useCapabilities } from "../library/capabilities/useCapabilities";
import { useSessionWorkspace } from "./useSessionWorkspace";
import { useTaskStore } from "./useTaskStore";
import { RunBar, type RunBarProps, type RunBarView } from "./RunBar/RunBar";
import { runOrdinal } from "./RunBar/runBarFormat";
import { MetroMap } from "./Metro/MetroMap";
import { currentAttempt } from "./Metro/metroLayout";
import { RunTaskList } from "./RunList/RunTaskList";
import { TaskLens, type TaskDetailsMode } from "./Lens/TaskLens";
import { TaskGraphView } from "./GraphView/TaskGraphView";
import { blockedReason } from "./GraphView/graphStatus";
import { TaskDetailSheet } from "./DetailSheet/TaskDetailSheet";
import { BulkImportPanel } from "./BulkImport/BulkImportPanel";
import styles from "./TasksTab.module.css";

export interface TasksTabProps {
  sessionId: string;
  routerClient: RouterClient;
  pipelineClient: PipelineClient;
  /** Opens this task's detail sheet; a new object each time re-opens it. */
  focusTask?: { id: string };
  /** Shows this run (a run_seq), or Plan ("new"); a new object each time re-applies it. */
  focusRun?: { run: number | "new" };
  /** Opens a file the pipeline touched (the workspace routes it to the Changes tab). */
  onOpenFile?: (path: string) => void;
  /** The view on mount (the dev demo page's switch). */
  initialView?: RunBarView;
}

/**
 * Which run the tab shows. "auto" follows the live run while there is one,
 * else the newest past run, else Plan. "starting" is the gap between a
 * Start that went through and its pipeline.started arriving.
 */
type Selection = { kind: "auto" } | { kind: "plan" } | { kind: "starting" } | { kind: "run"; runSeq: number };

type Shown = { mode: "plan" } | { mode: "live" | "past"; runSeq: number } | { mode: "loading" };

type OpenSheet = { kind: "detail"; taskId: string; as: TaskDetailsMode } | { kind: "import" } | null;

interface RunFold {
  columns: string[];
  taskRunState: Record<string, TaskRunState>;
  excludedTaskIds: string[];
  template?: string;
}

const EMPTY_FOLD: RunFold = { columns: [], taskRunState: {}, excludedTaskIds: [] };
const NO_RUN_ID = "This run started before runs recorded their id, so it can't be stopped from here. Stop it from the CLI.";

function selectionFrom(focusRun: TasksTabProps["focusRun"]): Selection {
  if (!focusRun) return { kind: "auto" };
  return focusRun.run === "new" ? { kind: "plan" } : { kind: "run", runSeq: focusRun.run };
}

/**
 * The run menu's list: the history, with the live fold's own run folded in
 * (the history only refetches when the run starts or ends, and the metrics
 * projection can lag the event). A "running" record that isn't the live
 * run never got its end event — an older run shadowed by a newer one, or
 * one the engine died under — so it reads as interrupted.
 */
function mergeRuns(sessionId: string, history: PipelineRun[], live: UsePipelineRunResult, resolved: boolean): PipelineRun[] {
  const liveSeq = live.phase !== "idle" ? live.runStartSeq : undefined;
  const runs = history.map((r) =>
    r.outcome === "running" && r.run_seq !== liveSeq && resolved
      ? { ...r, outcome: "interrupted" as PipelineRunOutcome }
      : r,
  );
  if (liveSeq === undefined) return runs;
  const outcome = (live.phase === "running" ? "running" : (live.outcome ?? "completed")) as PipelineRunOutcome;
  const i = runs.findIndex((r) => r.run_seq === liveSeq);
  if (i !== -1) {
    runs[i] = { ...runs[i], outcome, ended_at: live.runEndedAt ?? runs[i].ended_at };
    return runs;
  }
  const entry: PipelineRun = {
    session_id: sessionId,
    run_seq: liveSeq,
    run_id: live.runId,
    started_at: live.runStartedAt ?? "",
    ended_at: live.runEndedAt,
    outcome,
    tasks_done: 0,
    tasks_blocked: 0,
    tasks_skipped: 0,
    cost_usd: 0,
  };
  return [entry, ...runs].sort((a, b) => b.run_seq - a.run_seq);
}

function templateNamed(templates: Template[], name: string | undefined): Template | undefined {
  if (!name) return undefined;
  return templates.find((t) => t.name === name) ?? templates.find((t) => t.id === name);
}

function templateLabel(template: Template | undefined, name: string | undefined): string | undefined {
  return template ? `${template.name} v${template.version}` : name;
}

/**
 * The Tasks tab (docs/TASKS-PIPELINE-DESIGN.md): the run bar over one of
 * two views of the run it shows. List is the metro line above the grouped
 * task list; focusing a task swaps the list for its lens. Graph is the
 * dependency graph, whose focus view draws the same lens. In Plan a click
 * opens the task's detail sheet; in a live or past run it zooms in.
 *
 * The detail and bulk-import sheets are one "which sheet is open" state,
 * rendered after the lens and graph so Esc closes the sheet first.
 */
export function TasksTab({
  sessionId,
  routerClient,
  pipelineClient,
  focusTask,
  focusRun,
  onOpenFile,
  initialView = "list",
}: TasksTabProps) {
  const { tasks, tasksById } = useTaskStore(routerClient, sessionId);
  const workspace = useSessionWorkspace(routerClient, sessionId);
  const { capabilities } = useCapabilities();
  const { templates, defaultTemplateId } = useRunTemplates(routerClient, sessionId);
  const live = usePipelineRun(sessionId, pipelineClient);
  const history = usePipelineRunHistory(sessionId, pipelineClient, true, `${live.phase}:${live.runStartSeq ?? ""}`);

  const [view, setView] = useState<RunBarView>(initialView);
  const [selection, setSelection] = useState<Selection>(() => selectionFrom(focusRun));
  const [focusTaskId, setFocusTaskId] = useState<string>();
  const [sheet, setSheet] = useState<OpenSheet>(null);
  // A task "+ Add task" just created, until the store's task.created refetch catches up.
  const [created, setCreated] = useState<Task>();
  const [actionError, setActionError] = useState<{ title: string; message: string }>();

  // Plan: in memory, per session. The template follows the workspace
  // default until the user picks one.
  const [excluded, setExcluded] = useState<ReadonlySet<string>>(() => new Set());
  const [pickedTemplateId, setPickedTemplateId] = useState<string>();
  useEffect(() => {
    setExcluded(new Set());
    setPickedTemplateId(undefined);
  }, [sessionId]);
  const templateId = pickedTemplateId ?? defaultTemplateId;

  useEffect(() => {
    if (focusTask) setSheet({ kind: "detail", taskId: focusTask.id, as: "details" });
  }, [focusTask]);

  useEffect(() => {
    if (!focusRun) return;
    setSelection(selectionFrom(focusRun));
    setFocusTaskId(undefined);
  }, [focusRun]);

  const liveSeq = live.phase === "running" ? live.runStartSeq : undefined;
  const resolved = !live.resuming && history.loaded;
  const runs = useMemo(
    () => mergeRuns(sessionId, history.runs, live, resolved),
    // live is a new object every render; these are the fields mergeRuns reads.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [sessionId, history.runs, resolved, live.phase, live.runStartSeq, live.outcome, live.runEndedAt, live.runStartedAt, live.runId],
  );

  // Start went through: follow the run once its pipeline.started lands, and
  // keep showing it (as past) once it ends.
  useEffect(() => {
    if (selection.kind === "starting" && liveSeq !== undefined) setSelection({ kind: "run", runSeq: liveSeq });
  }, [selection.kind, liveSeq]);

  let shown: Shown;
  if (selection.kind === "plan") shown = { mode: "plan" };
  else if (selection.kind === "starting") shown = liveSeq !== undefined ? { mode: "live", runSeq: liveSeq } : { mode: "plan" };
  else if (selection.kind === "run")
    shown = { mode: selection.runSeq === liveSeq ? "live" : "past", runSeq: selection.runSeq };
  else if (liveSeq !== undefined) shown = { mode: "live", runSeq: liveSeq };
  else if (!resolved) shown = { mode: "loading" };
  else {
    const newest = runs.find((r) => r.outcome !== "running");
    shown = newest ? { mode: "past", runSeq: newest.run_seq } : { mode: "plan" };
  }

  const shownMode = shown.mode;
  const shownSeq = shown.mode === "live" || shown.mode === "past" ? shown.runSeq : undefined;
  // A past run the live fold already holds (it just ended) needs no refetch.
  const foldIsLive = shownSeq !== undefined && shownSeq === live.runStartSeq && live.phase !== "idle";
  const { selectRun: loadPastRun } = history;
  useEffect(() => {
    if (shownMode === "past" && shownSeq !== undefined && !foldIsLive) loadPastRun(shownSeq);
  }, [shownMode, shownSeq, foldIsLive, loadPastRun]);

  const fold: RunFold = foldIsLive
    ? live
    : shown.mode === "past" && history.foldedRunSeq === shown.runSeq
      ? history
      : EMPTY_FOLD;

  const mode: RunMode = shown.mode === "live" || shown.mode === "past" ? shown.mode : "plan";
  const planTemplate = templates.find((t) => t.id === templateId);
  const runTemplate = mode === "plan" ? planTemplate : templateNamed(templates, fold.template);
  const stages = useMemo(() => runStages(runTemplate, fold.columns), [runTemplate, fold.columns]);
  const foldExcluded = fold.excludedTaskIds;
  const runExcluded = useMemo(() => new Set(foldExcluded), [foldExcluded]);
  const scopeExcluded = mode === "plan" ? excluded : runExcluded;
  const taskRunState = mode === "plan" ? EMPTY_FOLD.taskRunState : fold.taskRunState;

  // Interrupted tasks (docs/PIPELINE-LEASES-DESIGN.md): Plan asks where a run
  // of the chosen template picks each up; a live run, where it will. A past
  // run reads them from its own record instead.
  const pickupTemplateId = mode === "plan" ? templateId : mode === "live" ? runTemplate?.id : undefined;
  const pickup = usePickup(
    pipelineClient,
    sessionId,
    pickupTemplateId || undefined,
    tasks,
    `${live.phase}:${live.runStartSeq ?? ""}`,
  );
  const interrupted = useMemo(() => interruptedIds(pickup), [pickup]);
  const runLabelOf = useCallback((runSeq: number) => `Run ${runOrdinal(runs, runSeq)}`, [runs]);

  const blockReasons = useMemo(() => {
    const out: Record<string, string> = {};
    for (const t of tasks) {
      const reason = blockedReason(t, tasksById, taskRunState);
      if (reason) out[t.id] = reason;
    }
    return out;
  }, [tasks, tasksById, taskRunState]);

  // Leaving a run for Plan drops the zoom: Plan opens the detail sheet instead.
  useEffect(() => {
    if (mode === "plan") setFocusTaskId(undefined);
  }, [mode]);

  const reportError = useCallback((title: string, err: unknown) => {
    setActionError({ title, message: err instanceof Error ? err.message : String(err) });
  }, []);

  const toggleInclude = useCallback((id: string) => {
    setExcluded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  const retry = useCallback(
    (id: string) => {
      pipelineClient.retryTask(id).catch((err) => reportError("Couldn't retry the task", err));
    },
    [pipelineClient, reportError],
  );

  const openDetails = useCallback((taskId: string, as: TaskDetailsMode = "details") => {
    setSheet({ kind: "detail", taskId, as });
  }, []);

  function selectRow(id: string) {
    if (mode === "plan") openDetails(id);
    else setFocusTaskId(id);
  }

  function wontDo(id: string) {
    const task = tasksById[id];
    if (!task) return;
    routerClient.updateTask({ ...task, status: "wont_do" }).catch((err) => reportError("Couldn't mark it won't do", err));
  }

  async function addTask() {
    try {
      // A draft, so a live run can't pick it up before it's written.
      // source is required by the Data Service (a 400 without it).
      const task = await routerClient.createTask({ session_id: sessionId, title: "New task", draft: true, source: "user" });
      setCreated(task);
      openDetails(task.id);
    } catch (err) {
      reportError("Couldn't add a task", err);
    }
  }

  function selectRun(run: number | "new") {
    setFocusTaskId(undefined);
    setSelection(run === "new" ? { kind: "plan" } : { kind: "run", runSeq: run });
  }

  const common = {
    view,
    onViewChange: setView,
    runs,
    selectedRunSeq: shownSeq,
    onSelectRun: selectRun,
    templateLabelOf: (run: PipelineRun) =>
      run.run_seq === shownSeq ? templateLabel(runTemplate, fold.template) : undefined,
  };
  let runBar: RunBarProps | undefined;
  if (shown.mode === "plan") {
    runBar = {
      ...common,
      mode: "plan",
      templates,
      templateId,
      defaultTemplateId,
      onTemplateChange: setPickedTemplateId,
      runnableCount: runnableInPlan(tasks, tasksById, excluded, interrupted).length,
      warning: planWarning(tasks, tasksById, excluded, interrupted),
      onStart: async () => {
        const excludeTaskIds = [...excluded].filter((id) => tasksById[id]);
        await pipelineClient.startPipeline(sessionId, templateId, { excludeTaskIds });
        setSelection({ kind: "starting" });
      },
      onOpenLibrary: () => requestOpenLibrary("templates"),
    };
  } else if (shown.mode === "live") {
    const runId = live.runId;
    runBar = {
      ...common,
      mode: "live",
      templateLabel: templateLabel(runTemplate, fold.template),
      startedAt: live.runStartedAt,
      onStop: async () => {
        if (runId) await pipelineClient.cancelPipeline(runId);
      },
      stopDisabledReason: runId ? undefined : NO_RUN_ID,
    };
  } else if (shown.mode === "past") {
    const run = runs.find((r) => r.run_seq === shown.runSeq);
    runBar = {
      ...common,
      mode: "past",
      outcome: run?.outcome ?? "finished",
      templateLabel: templateLabel(runTemplate, fold.template),
      startedAt: run?.started_at || undefined,
      endedAt: run?.ended_at,
      onNewRun: () => selectRun("new"),
    };
  }

  const focusTaskObj = focusTaskId && mode !== "plan" ? tasksById[focusTaskId] : undefined;
  const renderLens = (taskId: string, showCloseHint = true) => {
    const task = tasksById[taskId];
    if (!task) return null;
    return (
      <TaskLens
        key={taskId}
        task={task}
        tasksById={tasksById}
        runStatus={runStatusOf(task, { mode, tasksById, excluded: scopeExcluded, taskRunState, interrupted })}
        stages={stages}
        runState={taskRunState[taskId]}
        sessionId={sessionId}
        pipelineClient={pipelineClient}
        runSeq={shownSeq}
        live={mode === "live"}
        onClose={() => setFocusTaskId(undefined)}
        onOpenDetails={openDetails}
        onOpenFile={onOpenFile}
        onRetry={retry}
        onWontDo={wontDo}
        blockReason={blockReasons[taskId]}
        runLabelOf={runLabelOf}
        pickupText={mode === "past" ? undefined : pickupLine(taskId, pickup)}
        showCloseHint={showCloseHint}
      />
    );
  };

  const sheetTask =
    sheet?.kind === "detail" ? (tasksById[sheet.taskId] ?? (created?.id === sheet.taskId ? created : undefined)) : undefined;
  const sheetRunState = sheetTask ? live.taskRunState[sheetTask.id] : undefined;
  // Read-only only while the live run is working on the task (it holds the
  // lease, so a write would be refused). An Interrupted task, or one a
  // person set in progress, has no live lease and stays editable.
  const sheetLocked = sheetTask?.status === "in_progress" && live.phase === "running" && !!sheetRunState;
  const sheetRetry = sheet?.kind === "detail" && sheet.as === "retry";

  return (
    <div className={styles.tab}>
      <ContentColumn variant="wide" className={styles.body}>
        {runBar && <RunBar {...runBar} />}

        <div className={[styles.content, view === "graph" && styles.graph].filter(Boolean).join(" ")}>
          {shown.mode === "loading" ? null : view === "list" ? (
            <>
              <MetroMap
                tasks={tasks}
                tasksById={tasksById}
                mode={mode}
                excluded={scopeExcluded}
                taskRunState={taskRunState}
                stages={stages}
                focusTaskId={focusTaskObj?.id}
                onSelectTask={selectRow}
                interrupted={interrupted}
              />
              {focusTaskObj ? (
                renderLens(focusTaskObj.id)
              ) : (
                <RunTaskList
                  tasks={tasks}
                  tasksById={tasksById}
                  mode={mode}
                  excluded={scopeExcluded}
                  taskRunState={taskRunState}
                  pickup={pickup}
                  onSelectTask={selectRow}
                  blockReasons={blockReasons}
                  onToggleInclude={toggleInclude}
                  onConfirmDraft={(id) =>
                    void routerClient.confirmTask(id).catch((err) => reportError("Couldn't confirm the draft", err))
                  }
                  onRetry={retry}
                  onEditTask={(id) => openDetails(id)}
                  onAddTask={() => void addTask()}
                  onBulkImport={() => setSheet({ kind: "import" })}
                />
              )}
            </>
          ) : (
            <TaskGraphView
              sessionId={sessionId}
              routerClient={routerClient}
              tasks={tasks}
              tasksById={tasksById}
              onSelectTask={(task) => openDetails(task.id)}
              mode={mode}
              excluded={scopeExcluded}
              stages={stages}
              taskRunState={taskRunState}
              pickup={pickup}
              runLabel={shownSeq !== undefined ? `Run ${runOrdinal(runs, shownSeq)}` : "Run"}
              onToggleInclude={toggleInclude}
              onEditRetry={(id) => openDetails(id, "retry")}
              onRetry={retry}
              focusTaskId={focusTaskObj?.id}
              onFocusTask={setFocusTaskId}
              // The focus breadcrumb already offers "Esc back to graph".
              renderFocus={(id) => renderLens(id, false)}
            />
          )}
        </div>
      </ContentColumn>

      {sheet?.kind === "detail" && sheetTask && (
        <TaskDetailSheet
          key={`${sheetTask.id}|${sheet.as}`}
          task={sheetTask}
          tasksById={tasksById}
          sessionTasks={tasks}
          routerClient={routerClient}
          onClose={() => setSheet(null)}
          pipelineClient={pipelineClient}
          onSelectTask={(task) => openDetails(task.id)}
          workspace={workspace}
          capabilities={capabilities}
          readOnly={sheetLocked}
          lockedAt={
            sheetLocked && sheetRunState?.currentStage
              ? { stage: sheetRunState.currentStage, attempt: Math.max(1, currentAttempt(sheetRunState)) }
              : undefined
          }
          pinnedReason={sheetRetry ? blockReasons[sheetTask.id] : undefined}
          primaryLabel={sheetRetry ? "Save & retry" : undefined}
          onSaved={
            sheetRetry
              ? (saved) => {
                  setSheet(null);
                  retry(saved.id);
                }
              : undefined
          }
        />
      )}

      {sheet?.kind === "import" && (
        <BulkImportPanel sessionId={sessionId} routerClient={routerClient} onClose={() => setSheet(null)} />
      )}

      {actionError && (
        <Toast tone="bad" title={actionError.title} description={actionError.message} onDismiss={() => setActionError(undefined)} />
      )}
    </div>
  );
}
