import { router, ROUTER_BASE, type RouterClient } from "../../router/client";
import type { Envelope, PipelineRun, Task } from "../../router/types";
import { FOLD_EVENT_TYPES } from "../state/usePipelineRun";

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
  streamTaskOutput(sessionId: string, taskId: string): PipelineOutputSource;
  // Historical run replay (HANDOFF.md §4) — the Pipeline tab's run picker.
  listRuns(sessionId: string): Promise<PipelineRun[]>;
  getRunEvents(sessionId: string, since: number): Promise<Envelope[]>;
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

  streamTaskOutput(sessionId: string, taskId: string): PipelineOutputSource {
    const es = new EventSource(
      `${ROUTER_BASE}/sessions/${sessionId}/pipeline/tasks/${taskId}/output`,
    );
    es.addEventListener("end", () => es.close());
    return {
      onChunk(listener: (chunk: string) => void): () => void {
        const handler = (ev: MessageEvent<string>) => listener(JSON.parse(ev.data) as string);
        es.addEventListener("message", handler);
        return () => es.removeEventListener("message", handler);
      },
      close(): void {
        es.close();
      },
    };
  }

  async listRuns(sessionId: string): Promise<PipelineRun[]> {
    return this.client.metricsPipelineRuns(sessionId);
  }

  async getRunEvents(sessionId: string, since: number): Promise<Envelope[]> {
    return this.client.getEvents({ sessionId, since, types: FOLD_EVENT_TYPES });
  }
}

// Stateless — safe as a module-level singleton rather than re-created per render.
export const pipelineClient: PipelineClient = new FetchPipelineClient();
