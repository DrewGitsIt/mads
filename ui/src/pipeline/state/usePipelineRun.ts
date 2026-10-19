import { useCallback, useEffect, useReducer, useRef, useState } from "react";
import { subscribeToEvents } from "../../router/events";
import { WORKSPACE_SESSION_EVENT_TYPES } from "../../router/sessionEventTypes";
import type { Envelope, PipelineRun, Task } from "../../router/types";
import type { PipelineClient } from "../client/pipelineClient";

// Task.status_changed/updated/created and note.created carry no full payload
// worth folding — refetch the whole list, same idiom as useSessionsList.
const METADATA_EVENT_TYPES = ["task.created", "task.updated", "task.status_changed", "note.created"];

// pipeline.*/stage.*/file.changed have no persisted read model (HANDOFF.md
// §2) — folded into local reducer state instead of refetched. Exported so
// usePipelineRunHistory (HANDOFF.md §4b) can feed the same fold a batch of
// historical envelopes instead of a live SSE stream — the fold itself is
// transport-agnostic.
export const FOLD_EVENT_TYPES = [
  "pipeline.started",
  "pipeline.completed",
  "pipeline.cancelled",
  "pipeline.failed",
  "stage.started",
  "stage.completed",
  "stage.failed",
  "stage.skipped",
  "stage.retried",
  "file.changed",
];

export type RunPhase = "idle" | "running" | "finished";
export type StageAttemptStatus = "started" | "completed" | "failed" | "skipped";

export interface StageAttempt {
  stage: string;
  /**
   * 1-based count of stage.started events (stage ENTRIES) for this stage on
   * this task — i.e. how many times the pipeline has loop-backed into this
   * stage. Policy retries (structural failure: timeout, provider crash) do
   * NOT emit stage.started — they happen inside a single stage entry and are
   * counted separately by `retries` below, not folded into this number.
   */
  attempt: number;
  status: StageAttemptStatus;
  /** stage.failed's `reason` — the loop-back/blocker feedback text (HANDOFF.md §1c). */
  reason?: string;
  /**
   * Policy retries that ran inside this stage entry (structural failure:
   * timeout, provider crash) — distinct from `attempt`, which counts stage
   * entries/loop-backs. Undefined/0 means the entry ran to completion (or is
   * still running) without a structural retry.
   */
  retries?: number;
  startedSeq: number;
  endedSeq?: number;
  /** The opening envelope's timestamp (stage.started, or stage.skipped). */
  startedAt?: string;
  /** The closing envelope's timestamp; unset while the attempt is still open. */
  endedAt?: string;
}

export interface TaskRunState {
  taskId: string;
  /** Stage of the most recent stage.started for this task this run; the board column. */
  currentStage?: string;
  attempts: StageAttempt[];
  touchedFiles: string[];
}

export interface RunFoldState {
  phase: RunPhase;
  outcome?: string;
  runStartSeq?: number;
  runStartedAt?: string;
  runEndedAt?: string;
  /**
   * pipeline.started's `run_id` — the Router's id, the one cancelPipeline
   * takes. Undefined for runs recorded before the event carried it.
   */
  runId?: string;
  /** pipeline.started's `template` — the template's name as the engine parsed it. */
  template?: string;
  /** pipeline.started's `excluded_task_ids`: tasks left out of this run. Empty for older runs. */
  excludedTaskIds: string[];
  /** Stage names in first-seen order across the current run — the board's column order. */
  columns: string[];
  taskRunState: Record<string, TaskRunState>;
}

export const initialRunFoldState: RunFoldState = {
  phase: "idle",
  excludedTaskIds: [],
  columns: [],
  taskRunState: {},
};

function taskRunStateOf(state: RunFoldState, taskId: string): TaskRunState {
  return state.taskRunState[taskId] ?? { taskId, attempts: [], touchedFiles: [] };
}

// Shared by the stage.completed/stage.failed close-out and stage.retried
// bump below — both need "the most recent attempt for this stage that's
// still open" (status === "started").
function findOpenAttemptIndex(attempts: StageAttempt[], stage: string): number {
  for (let i = attempts.length - 1; i >= 0; i--) {
    if (attempts[i].stage === stage && attempts[i].status === "started") {
      return i;
    }
  }
  return -1;
}

function foldStageEvent(state: RunFoldState, env: Envelope): RunFoldState {
  if (state.phase !== "running" || state.runStartSeq === undefined || env.seq < state.runStartSeq) {
    return state;
  }
  const taskId = env.task_id;
  const payload = env.payload as { stage?: string; reason?: string } | undefined;
  if (!taskId || !payload?.stage) return state;
  const stage = payload.stage;

  const existing = taskRunStateOf(state, taskId);
  let attempts = existing.attempts;
  let currentStage = existing.currentStage;

  if (env.type === "stage.started" || env.type === "stage.skipped") {
    const attemptNumber = attempts.filter((a) => a.stage === stage).length + 1;
    const status: StageAttemptStatus = env.type === "stage.started" ? "started" : "skipped";
    attempts = [
      ...attempts,
      {
        stage,
        attempt: attemptNumber,
        status,
        startedSeq: env.seq,
        endedSeq: status === "skipped" ? env.seq : undefined,
        startedAt: env.timestamp,
        endedAt: status === "skipped" ? env.timestamp : undefined,
      },
    ];
    if (env.type === "stage.started") currentStage = stage;
  } else if (env.type === "stage.retried") {
    // Retry re-runs the same stage entry in place — no stage.started, so no
    // new attempt. Bump `retries` on the open attempt; if none is open
    // (e.g. this envelope arrived before the run's seeded prefix), no-op.
    const openIndex = findOpenAttemptIndex(attempts, stage);
    if (openIndex !== -1) {
      attempts = attempts.slice();
      attempts[openIndex] = {
        ...attempts[openIndex],
        retries: (attempts[openIndex].retries ?? 0) + 1,
      };
    }
  } else {
    // stage.completed | stage.failed — close out the most recent open attempt for this stage.
    const openIndex = findOpenAttemptIndex(attempts, stage);
    if (openIndex !== -1) {
      const status: StageAttemptStatus = env.type === "stage.completed" ? "completed" : "failed";
      attempts = attempts.slice();
      attempts[openIndex] = {
        ...attempts[openIndex],
        status,
        endedSeq: env.seq,
        endedAt: env.timestamp,
        reason: env.type === "stage.failed" ? payload.reason : undefined,
      };
    }
  }

  const columns = state.columns.includes(stage) ? state.columns : [...state.columns, stage];

  return {
    ...state,
    columns,
    taskRunState: {
      ...state.taskRunState,
      [taskId]: { ...existing, attempts, currentStage },
    },
  };
}

function foldFileChanged(state: RunFoldState, env: Envelope): RunFoldState {
  if (state.phase !== "running" || state.runStartSeq === undefined || env.seq < state.runStartSeq) {
    return state;
  }
  const taskId = env.task_id;
  const payload = env.payload as { path?: string } | undefined;
  if (!taskId || !payload?.path) return state;

  const existing = taskRunStateOf(state, taskId);
  if (existing.touchedFiles.includes(payload.path)) return state;

  return {
    ...state,
    taskRunState: {
      ...state.taskRunState,
      [taskId]: { ...existing, touchedFiles: [...existing.touchedFiles, payload.path] },
    },
  };
}

export function foldReducer(state: RunFoldState, env: Envelope): RunFoldState {
  switch (env.type) {
    case "pipeline.started": {
      // A new run always resets board state — old runs' stage.*/task.* events
      // for the same task ids are otherwise indistinguishable (HANDOFF.md §2).
      const payload = env.payload as
        | { run_id?: string; template?: string; excluded_task_ids?: string[] | null }
        | undefined;
      return {
        phase: "running",
        runStartSeq: env.seq,
        runStartedAt: env.timestamp,
        runId: payload?.run_id || undefined,
        template: payload?.template || undefined,
        excludedTaskIds: payload?.excluded_task_ids ?? [],
        columns: [],
        taskRunState: {},
      };
    }
    case "pipeline.completed":
    case "pipeline.cancelled":
    case "pipeline.failed": {
      if (state.runStartSeq === undefined || env.seq < state.runStartSeq) return state;
      const payload = env.payload as { outcome?: string; run_id?: string } | undefined;
      // Another run's end (e.g. the Router marking an older run lost at
      // startup) must not finish this one.
      if (payload?.run_id && state.runId && payload.run_id !== state.runId) return state;
      return {
        ...state,
        phase: "finished",
        outcome: payload?.outcome ?? env.type.slice("pipeline.".length),
        runEndedAt: env.timestamp,
      };
    }
    case "stage.started":
    case "stage.completed":
    case "stage.failed":
    case "stage.skipped":
    case "stage.retried":
      return foldStageEvent(state, env);
    case "file.changed":
      return foldFileChanged(state, env);
    default:
      return state;
  }
}

// Wraps foldReducer so the same useReducer can also accept a pre-folded
// seed state (the historical prefix of an in-flight run) without changing
// foldReducer's own signature, which usePipelineRunHistory.ts depends on.
type RunAction = { kind: "envelope"; env: Envelope } | { kind: "seed"; state: RunFoldState };

function runReducer(state: RunFoldState, action: RunAction): RunFoldState {
  return action.kind === "seed" ? action.state : foldReducer(state, action.env);
}

export interface UsePipelineRunResult {
  tasks: Task[];
  loading: boolean;
  error: string | null;
  resuming: boolean;
  phase: RunPhase;
  outcome?: string;
  runStartSeq?: number;
  runStartedAt?: string;
  runEndedAt?: string;
  /** The run's Router id, from the fold — so Cancel works after a remount or reload. */
  runId?: string;
  template?: string;
  excludedTaskIds: string[];
  columns: string[];
  taskRunState: Record<string, TaskRunState>;
  refetchTasks: () => Promise<void>;
}

/**
 * Shared resume-then-subscribe fold: buffers live envelopes behind a flag
 * while REST catch-up (listRuns -> pickRun -> getRunEvents) seeds the fold
 * with the historical prefix, then flushes the buffer past the caught-up
 * seq. Extracted so usePipelineRun and useGraphRunState can't drift the way
 * usePipelineRun's own resume fix once did (HANDOFF.md) — pickRun is the
 * only thing that differs between callers' notion of "the run to resume".
 */
export function useResumingRunFold(
  sessionId: string,
  client: PipelineClient | undefined,
  pickRun: (runs: PipelineRun[]) => PipelineRun | undefined,
  onEnvelope?: (env: Envelope) => void,
): { state: RunFoldState; resuming: boolean } {
  const [state, dispatch] = useReducer(runReducer, initialRunFoldState);
  const [resuming, setResuming] = useState(true);
  const mounted = useRef(true);
  const clientRef = useRef(client);
  clientRef.current = client;
  const pickRunRef = useRef(pickRun);
  pickRunRef.current = pickRun;
  const onEnvelopeRef = useRef(onEnvelope);
  onEnvelopeRef.current = onEnvelope;

  useEffect(() => {
    const client = clientRef.current;
    if (!client) return;
    mounted.current = true;
    let unsubscribe: (() => void) | undefined;

    function handleEnvelope(env: Envelope) {
      if (FOLD_EVENT_TYPES.includes(env.type)) dispatch({ kind: "envelope", env });
      onEnvelopeRef.current?.(env);
    }

    async function resumeAndSubscribe() {
      // Register with the bus before the REST catch-up starts, buffering
      // incoming envelopes locally instead of folding them yet — this closes
      // the race between "what REST returns" and "what arrives live while
      // REST is in flight" without needing a dedicated since-scoped
      // connection outside the shared bus (HANDOFF.md §3).
      const buffered: Envelope[] = [];
      let live = false;
      unsubscribe = subscribeToEvents(
        { sessionId, types: WORKSPACE_SESSION_EVENT_TYPES },
        (env) => {
          if (live) handleEnvelope(env);
          else buffered.push(env);
        },
      );

      let caughtUpSeq = 0;
      try {
        const runs = await client!.listRuns(sessionId);
        const target = pickRunRef.current(runs);
        if (target) {
          const events = await client!.getRunEvents(sessionId, target.run_seq - 1);
          if (!mounted.current) return;
          let seeded = events.reduce(foldReducer, initialRunFoldState);
          // The run history pairs runs by id, so it can know the id even when
          // the fetched prefix didn't start at pipeline.started.
          if (seeded.runId === undefined && target.run_id && seeded.runStartSeq === target.run_seq) {
            seeded = { ...seeded, runId: target.run_id };
          }
          dispatch({ kind: "seed", state: seeded });
          caughtUpSeq = events.length > 0 ? Math.max(...events.map((e) => e.seq)) : target.run_seq - 1;
        }
      } catch {
        // Best-effort: fall through to a live-only tail, same as today's behavior.
      }
      if (!mounted.current) return;
      setResuming(false);
      live = true;
      // Flush what arrived live during catch-up, deduping the overlap edge
      // against what REST already returned.
      for (const env of buffered) {
        if (env.seq > caughtUpSeq) handleEnvelope(env);
      }
    }

    void resumeAndSubscribe();
    return () => {
      mounted.current = false;
      unsubscribe?.();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId, !!client]);

  return { state, resuming };
}

// Seeds the task list and keeps it live via refetch-on-event
// (useSessionsList's idiom), while separately folding pipeline/stage/
// file.changed envelopes into local board state — there's no read endpoint
// for "current stage per task" to refetch instead (HANDOFF.md §2).
export function usePipelineRun(sessionId: string, client: PipelineClient): UsePipelineRunResult {
  const [tasks, setTasks] = useState<Task[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const mounted = useRef(true);

  const refetchTasks = useCallback(async () => {
    try {
      const list = await client.listTasks(sessionId);
      if (mounted.current) setTasks(list);
    } catch (err) {
      if (mounted.current) setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (mounted.current) setLoading(false);
    }
  }, [client, sessionId]);

  useEffect(() => {
    mounted.current = true;
    void refetchTasks();
    return () => {
      mounted.current = false;
    };
  }, [refetchTasks]);

  const onEnvelope = useCallback(
    (env: Envelope) => {
      if (METADATA_EVENT_TYPES.includes(env.type)) void refetchTasks();
    },
    [refetchTasks],
  );

  // listRuns returns runs oldest-first; a stuck/stale "running" record from
  // an earlier, never-terminated run must not shadow a genuinely newer one.
  const pickRun = useCallback(
    (runs: PipelineRun[]) =>
      runs.filter((r) => r.outcome === "running").sort((a, b) => b.run_seq - a.run_seq)[0],
    [],
  );

  const { state: runState, resuming } = useResumingRunFold(sessionId, client, pickRun, onEnvelope);

  return {
    tasks,
    loading,
    error,
    resuming,
    phase: runState.phase,
    outcome: runState.outcome,
    runStartSeq: runState.runStartSeq,
    runStartedAt: runState.runStartedAt,
    runEndedAt: runState.runEndedAt,
    runId: runState.runId,
    template: runState.template,
    excludedTaskIds: runState.excludedTaskIds,
    columns: runState.columns,
    taskRunState: runState.taskRunState,
    refetchTasks,
  };
}
