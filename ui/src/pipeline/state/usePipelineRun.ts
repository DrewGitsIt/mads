import { useCallback, useEffect, useReducer, useRef, useState } from "react";
import { subscribeToEvents } from "../../router/events";
import { WORKSPACE_SESSION_EVENT_TYPES } from "../../router/sessionEventTypes";
import type { Envelope, Task } from "../../router/types";
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
  "file.changed",
];

export type RunPhase = "idle" | "running" | "finished";
export type StageAttemptStatus = "started" | "completed" | "failed" | "skipped";

export interface StageAttempt {
  stage: string;
  /** 1-based count of stage.started events for this stage on this task. */
  attempt: number;
  status: StageAttemptStatus;
  /** stage.failed's `reason` — the loop-back/blocker feedback text (HANDOFF.md §1c). */
  reason?: string;
  startedSeq: number;
  endedSeq?: number;
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
  /** Stage names in first-seen order across the current run — the board's column order. */
  columns: string[];
  taskRunState: Record<string, TaskRunState>;
}

export const initialRunFoldState: RunFoldState = {
  phase: "idle",
  columns: [],
  taskRunState: {},
};

function taskRunStateOf(state: RunFoldState, taskId: string): TaskRunState {
  return state.taskRunState[taskId] ?? { taskId, attempts: [], touchedFiles: [] };
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
      },
    ];
    if (env.type === "stage.started") currentStage = stage;
  } else {
    // stage.completed | stage.failed — close out the most recent open attempt for this stage.
    let openIndex = -1;
    for (let i = attempts.length - 1; i >= 0; i--) {
      if (attempts[i].stage === stage && attempts[i].status === "started") {
        openIndex = i;
        break;
      }
    }
    if (openIndex !== -1) {
      const status: StageAttemptStatus = env.type === "stage.completed" ? "completed" : "failed";
      attempts = attempts.slice();
      attempts[openIndex] = {
        ...attempts[openIndex],
        status,
        endedSeq: env.seq,
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
    case "pipeline.started":
      // A new run always resets board state — old runs' stage.*/task.* events
      // for the same task ids are otherwise indistinguishable (HANDOFF.md §2).
      return {
        phase: "running",
        runStartSeq: env.seq,
        runStartedAt: env.timestamp,
        columns: [],
        taskRunState: {},
      };
    case "pipeline.completed":
    case "pipeline.cancelled":
    case "pipeline.failed": {
      if (state.runStartSeq === undefined || env.seq < state.runStartSeq) return state;
      const payload = env.payload as { outcome?: string } | undefined;
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
  columns: string[];
  taskRunState: Record<string, TaskRunState>;
  refetchTasks: () => Promise<void>;
}

// Seeds the task list and keeps it live via refetch-on-event
// (useSessionsList's idiom), while separately folding pipeline/stage/
// file.changed envelopes into local board state — there's no read endpoint
// for "current stage per task" to refetch instead (HANDOFF.md §2).
export function usePipelineRun(sessionId: string, client: PipelineClient): UsePipelineRunResult {
  const [tasks, setTasks] = useState<Task[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [resuming, setResuming] = useState(true);
  const [runState, dispatch] = useReducer(runReducer, initialRunFoldState);
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
    let unsubscribe: (() => void) | undefined;

    function handleEnvelope(env: Envelope) {
      if (FOLD_EVENT_TYPES.includes(env.type)) dispatch({ kind: "envelope", env });
      if (METADATA_EVENT_TYPES.includes(env.type)) void refetchTasks();
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
        const runs = await client.listRuns(sessionId);
        const inFlight = runs.find((r) => r.outcome === "running");
        if (inFlight) {
          const events = await client.getRunEvents(sessionId, inFlight.run_seq - 1);
          if (!mounted.current) return;
          const seeded = events.reduce(foldReducer, initialRunFoldState);
          dispatch({ kind: "seed", state: seeded });
          caughtUpSeq = events.length > 0 ? Math.max(...events.map((e) => e.seq)) : inFlight.run_seq - 1;
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
  }, [client, refetchTasks, sessionId]);

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
    columns: runState.columns,
    taskRunState: runState.taskRunState,
    refetchTasks,
  };
}
