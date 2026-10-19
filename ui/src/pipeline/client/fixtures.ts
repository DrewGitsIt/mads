import type {
  PipelineClient,
  PipelineOutputSource,
  PipelineTurnsSource,
  StartPipelineOptions,
  TaskStreamTarget,
} from "./pipelineClient";
import type { Envelope, PipelinePickup, PipelineRun, Task } from "../../router/types";
import { recordedEnvelopes, recordedSessionId, recordedTaskIds, recordedTasks } from "./recordedRun";

export const fixtureSessionId = recordedSessionId;

// ── An interrupted run, and the run that picks its task up ──
// (docs/PIPELINE-LEASES-DESIGN.md B1/B2), built from the recorded run.

/** The recorded run's last event before Stop: flaky api's third code_review attempt has just opened. */
export const STOPPED_AT_SEQ = 46;
/** The recorded run's pipeline.started seq: Run 1. */
export const STOPPED_RUN_SEQ = recordedEnvelopes.find((e) => e.type === "pipeline.started")!.seq;
/** The picking-up run's pipeline.started seq: Run 2. */
export const PICKUP_RUN_SEQ = STOPPED_AT_SEQ + 2;

/**
 * Run 1, stopped: the recording up to flaky api's third code_review
 * attempt, then pipeline.cancelled. flaky api is left in flight:
 * Interrupted.
 */
export function stoppedRunEnvelopes(): Envelope[] {
  const at = recordedEnvelopes.find((e) => e.seq === STOPPED_AT_SEQ)!.timestamp;
  return [
    ...recordedEnvelopes
      .filter((e) => e.seq <= STOPPED_AT_SEQ)
      .map((e) =>
        e.type === "pipeline.started"
          ? { ...e, payload: { ...(e.payload as object), run_id: "run_stopped", template_version: 3 } }
          : e,
      ),
    {
      seq: STOPPED_AT_SEQ + 1,
      timestamp: at,
      type: "pipeline.cancelled",
      session_id: recordedSessionId,
      actor: "router",
      payload: { run_id: "run_stopped" },
    },
  ];
}

/**
 * Run 1 stopped, then Run 2 picks flaky api up: same template and version,
 * so it resumes the interrupted code_review attempt (3), with the stages Run
 * 1 completed done.
 */
export function pickedUpRunEnvelopes(): Envelope[] {
  const stopped = stoppedRunEnvelopes();
  const at = stopped[stopped.length - 1].timestamp;
  const ev = (seq: number, type: string, payload: Record<string, unknown>, taskId?: string): Envelope => ({
    seq,
    timestamp: at,
    type,
    session_id: recordedSessionId,
    ...(taskId ? { task_id: taskId } : {}),
    actor: "pipeline",
    payload,
  });
  return [
    ...stopped,
    ev(PICKUP_RUN_SEQ, "pipeline.started", {
      run_id: "run_pickup",
      template: "default-implementation",
      template_version: 3,
    }),
    ev(
      PICKUP_RUN_SEQ + 1,
      "stage.picked_up",
      {
        run_seq: PICKUP_RUN_SEQ,
        mode: "resume",
        from_stage: "code_review",
        resumed_from_run_id: "run_stopped",
        resumed_from_run_seq: STOPPED_RUN_SEQ,
        completed_stages: ["architect", "executor", "build"],
      },
      recordedTaskIds.flakyApi,
    ),
    ev(
      PICKUP_RUN_SEQ + 2,
      "stage.started",
      { stage: "code_review", run_seq: PICKUP_RUN_SEQ, attempt: 3, resumed: true },
      recordedTaskIds.flakyApi,
    ),
  ];
}

/** The tasks as Run 1's Stop left them: flaky api in progress (Interrupted), cli waiting on it. */
export function stoppedRunTasks(): Task[] {
  return recordedTasks.map((t) => {
    const copy = structuredClone(t);
    if (copy.id === recordedTaskIds.flakyApi) {
      copy.status = "in_progress";
      copy.notes = (copy.notes ?? []).filter((n) => n.type !== "blocker");
    }
    return copy;
  });
}

/** What the pickup endpoint answers for the stopped run's task under the same template. */
export const stoppedRunPickup: PipelinePickup = {
  shared_directory: false,
  tasks: [
    {
      task_id: recordedTaskIds.flakyApi,
      mode: "resume",
      from_stage: "code_review",
      last_run_seq: STOPPED_RUN_SEQ,
      last_run_id: "run_stopped",
      template_name: "default-implementation",
      template_version: 3,
    },
  ],
};

/** Canned agent-output lines the demo mount and AgentOutputView.test.tsx replay. */
export const fixtureOutputChunks = [
  "Reading task context...\r\n",
  "Editing services/pipeline/engine/engine.go\r\n",
  "Running go test ./...\r\n",
  "STAGE: pass\r\n",
];

/**
 * Replays a canned string of chunks on a timer — the stand-in for a real
 * per-task output stream, which doesn't exist yet (HANDOFF.md §6). Used by
 * the demo mount and AgentOutputView.test.tsx.
 */
export class FakePipelineOutputSource implements PipelineOutputSource {
  private listeners = new Set<(chunk: string) => void>();
  private endListeners = new Set<() => void>();
  private ended = false;
  private timer?: ReturnType<typeof setInterval>;
  private index = 0;

  constructor(
    private readonly chunks: string[] = fixtureOutputChunks,
    private readonly intervalMs = 30,
  ) {}

  onChunk(listener: (chunk: string) => void): () => void {
    this.listeners.add(listener);
    this.start();
    return () => this.listeners.delete(listener);
  }

  /** Fires once every chunk has been replayed, like the server's `end`. */
  onEnd(listener: () => void): () => void {
    if (this.ended) listener();
    else this.endListeners.add(listener);
    return () => this.endListeners.delete(listener);
  }

  close(): void {
    if (this.timer) clearInterval(this.timer);
    this.timer = undefined;
  }

  private start(): void {
    if (this.timer) return;
    this.timer = setInterval(() => {
      if (this.index >= this.chunks.length) {
        this.close();
        this.ended = true;
        for (const listener of this.endListeners) listener();
        return;
      }
      const chunk = this.chunks[this.index++];
      for (const listener of this.listeners) listener(chunk);
    }, this.intervalMs);
  }
}

/**
 * Never delivers a turn — the demo mount and TasksTab tests have no
 * scripted provider transcript, so every fixture task exercises the
 * fallback-to-raw-output path (DrillInFeed), same as a real fake-provider
 * run.
 */
export class FakePipelineTurnsSource implements PipelineTurnsSource {
  onTurns(): () => void {
    return () => {};
  }

  close(): void {}
}

/**
 * In-memory PipelineClient, same shape as FakeRouterClient
 * (ui/src/router/fixtures.ts): array of fixture Task[], startPipeline
 * returns a fixed run_id, retryTask flips the fixture task back to ready.
 * The three unsupported methods still reject (HANDOFF.md §4), so a
 * component test can assert the corresponding buttons are disabled and
 * never call them, rather than silently succeeding against the fake in a
 * way the real client never would.
 */
export class FakePipelineClient implements PipelineClient {
  // Seedable by tests that exercise usePipelineRunHistory and the run menu — empty
  // by default so every pre-existing test (none of which touch runs
  // history) keeps seeing no runs, same as today.
  runs: PipelineRun[] = [];
  runEvents: Envelope[] = [];
  // Recorded so tests can assert what a run was started with and which log a
  // stream asked for, without spying.
  startCalls: { sessionId: string; templateId: string; opts?: StartPipelineOptions }[] = [];
  cancelCalls: string[] = [];
  // What getPickup answers: no Interrupted tasks unless a test or the demo says so.
  pickup: PipelinePickup = { shared_directory: false, tasks: [] };
  pickupCalls: { sessionId: string; templateId: string }[] = [];
  streamCalls: { kind: "output" | "turns"; taskId: string; target?: TaskStreamTarget }[] = [];
  // The chunks a stream replays; the canned lines unless this says otherwise
  // ([] is a log with no output: zero chunks, then the end).
  outputChunksFor?: (taskId: string, target?: TaskStreamTarget) => string[] | undefined;

  constructor(private tasks: Task[] = recordedTasks.map((t) => ({ ...t }))) {}

  async listTasks(sessionId: string): Promise<Task[]> {
    return this.tasks.filter((t) => t.session_id === sessionId);
  }

  async startPipeline(
    sessionId: string,
    templateId: string,
    opts?: StartPipelineOptions,
  ): Promise<{ run_id: string }> {
    this.startCalls.push({ sessionId, templateId, opts });
    return { run_id: "run_fixture" };
  }

  async cancelPipeline(runId: string): Promise<void> {
    this.cancelCalls.push(runId);
  }

  async getPickup(sessionId: string, templateId: string): Promise<PipelinePickup> {
    this.pickupCalls.push({ sessionId, templateId });
    return structuredClone(this.pickup);
  }

  async retryTask(taskId: string): Promise<Task> {
    const task = this.tasks.find((t) => t.id === taskId);
    if (!task) throw new Error(`fixture task not found: ${taskId}`);
    task.status = "ready";
    task.retry_count += 1;
    return { ...task };
  }

  async pausePipeline(_runId: string): Promise<void> {
    throw new Error("not supported by the current pipeline engine");
  }

  async resumePipeline(_runId: string): Promise<void> {
    throw new Error("not supported by the current pipeline engine");
  }

  async reassignTask(_taskId: string, _agent: string): Promise<void> {
    throw new Error("not supported by the current pipeline engine");
  }

  streamTaskOutput(_sessionId: string, taskId: string, target?: TaskStreamTarget): PipelineOutputSource {
    this.streamCalls.push({ kind: "output", taskId, target });
    return new FakePipelineOutputSource(this.outputChunksFor?.(taskId, target));
  }

  streamTaskTurns(_sessionId: string, taskId: string, target?: TaskStreamTarget): PipelineTurnsSource {
    this.streamCalls.push({ kind: "turns", taskId, target });
    return new FakePipelineTurnsSource();
  }

  async listRuns(_sessionId: string): Promise<PipelineRun[]> {
    return this.runs;
  }

  async getRunEvents(_sessionId: string, since: number): Promise<Envelope[]> {
    return this.runEvents.filter((env) => env.seq > since);
  }
}
