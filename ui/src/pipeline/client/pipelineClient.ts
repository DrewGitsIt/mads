import { router, type RouterClient } from "../../router/client";
import type { Task } from "../../router/types";

/**
 * A single task's read-only agent-output stream. Returned by
 * `streamTaskOutput`; consumed by `DrillIn/pipelineOutputBridge.ts`, which
 * adapts it into the `PtyBridge` shape `TerminalView` expects.
 */
export interface PipelineOutputSource {
  /** Returns an unsubscribe function. */
  onChunk(listener: (chunk: string) => void): () => void;
  close(): void;
}

const NOT_SUPPORTED = "not supported by the current pipeline engine";

/**
 * Router API surface the Pipeline tab depends on — a dedicated interface
 * analogous to `RouterClient` (`ui/src/router/client.ts`) but scoped to
 * pipeline actions, per HANDOFF.md §4.
 */
export interface PipelineClient {
  listTasks(sessionId: string): Promise<Task[]>;
  startPipeline(sessionId: string, templateId: string): Promise<{ run_id: string }>;
  cancelPipeline(runId: string): Promise<void>;
  retryTask(taskId: string): Promise<Task>;
  // Both reject — no backend capability exists (HANDOFF.md §1: the engine
  // has no pause hook, and there's no per-task agent-override mechanism).
  // Wired so the interface documents the eventual contract; the UI disables
  // the corresponding buttons rather than ever calling these against the
  // real client.
  pausePipeline(runId: string): Promise<void>;
  resumePipeline(runId: string): Promise<void>;
  reassignTask(taskId: string, agent: string): Promise<void>;
  streamTaskOutput(runId: string, taskId: string): PipelineOutputSource;
}

/** RouterClient-backed PipelineClient. Only exercised directly by pipelineClient.test.ts. */
export class FetchPipelineClient implements PipelineClient {
  constructor(private readonly client: RouterClient = router) {}

  async listTasks(sessionId: string): Promise<Task[]> {
    return this.client.listTasks(sessionId);
  }

  async startPipeline(
    sessionId: string,
    templateId: string,
  ): Promise<{ run_id: string }> {
    return this.client.createPipelineRun(sessionId, templateId);
  }

  async cancelPipeline(runId: string): Promise<void> {
    await this.client.cancelPipelineRun(runId);
  }

  async retryTask(taskId: string): Promise<Task> {
    return this.client.retryTask(taskId);
  }

  async pausePipeline(_runId: string): Promise<void> {
    throw new Error(NOT_SUPPORTED);
  }

  async resumePipeline(_runId: string): Promise<void> {
    throw new Error(NOT_SUPPORTED);
  }

  async reassignTask(_taskId: string, _agent: string): Promise<void> {
    throw new Error(NOT_SUPPORTED);
  }

  /**
   * `services/pipeline`'s HTTP server isn't wired to the engine yet, and
   * even once it is, `GET /pipelines/{id}/output` has no per-task tagging
   * (HANDOFF.md §6) — there is nothing to subscribe to. Returns an inert
   * source that never emits, rather than hitting a route that can't answer
   * the question "which task's output is this."
   */
  streamTaskOutput(_runId: string, _taskId: string): PipelineOutputSource {
    return {
      onChunk: () => () => {},
      close: () => {},
    };
  }
}
