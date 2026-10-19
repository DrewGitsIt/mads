import type {
  PipelineClient,
  PipelineOutputSource,
  PipelineTurnsSource,
  StartPipelineOptions,
  TaskStreamTarget,
} from "./pipelineClient";
import type { Envelope, PipelineRun, Task } from "../../router/types";
import { recordedSessionId, recordedTasks } from "./recordedRun";

export const fixtureSessionId = recordedSessionId;

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

  close(): void {
    if (this.timer) clearInterval(this.timer);
    this.timer = undefined;
  }

  private start(): void {
    if (this.timer) return;
    this.timer = setInterval(() => {
      if (this.index >= this.chunks.length) {
        this.close();
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
  streamCalls: { kind: "output" | "turns"; taskId: string; target?: TaskStreamTarget }[] = [];

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

  async cancelPipeline(_runId: string): Promise<void> {}

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
    return new FakePipelineOutputSource();
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
