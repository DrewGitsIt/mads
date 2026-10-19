import { router, ROUTER_BASE, type RouterClient } from "../../router/client";
import { getAllEvents } from "../../router/eventPages";
import type { Envelope, PipelineRun, Task, TranscriptMessage } from "../../router/types";
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

/**
 * A single task's normalized-turns transcript stream (GET .../turns) — the
 * transparency-first drill-in feed, sourced from the provider's own
 * transcript rather than the tee'd prose log. Each frame is the full turn
 * array so far, not a delta; empty for tasks with no recorded provider
 * transcript (the caller's cue to fall back to streamTaskOutput).
 */
export interface PipelineTurnsSource {
  /** Returns an unsubscribe function. */
  onTurns(listener: (turns: TranscriptMessage[]) => void): () => void;
  close(): void;
}

/**
 * Which log a task stream reads (GET .../output and .../turns). Omitted →
 * the latest run, following the newest log. `run` alone → that run's newest
 * log for the task. `stage` + `attempt` → exactly that stage attempt's log
 * (turns: that attempt's recorded transcript). A log that isn't the one being
 * written streams its contents, then `end`; a missing one is zero chunks,
 * then `end`.
 */
export interface TaskStreamTarget {
  /** The run's run_seq (PipelineRun.run_seq), not its run_id. */
  run?: number;
  stage?: string;
  /** 1-based, StageAttempt.attempt. */
  attempt?: number;
}

export interface StartPipelineOptions {
  /** Ready tasks to leave out of the run. Sent only when non-empty. */
  excludeTaskIds?: string[];
}

function taskStreamUrl(
  sessionId: string,
  taskId: string,
  kind: "output" | "turns",
  target: TaskStreamTarget | undefined,
): string {
  const params = new URLSearchParams();
  if (target?.run !== undefined) params.set("run", String(target.run));
  if (target?.stage) params.set("stage", target.stage);
  if (target?.attempt !== undefined) params.set("attempt", String(target.attempt));
  const query = params.toString();
  return `${ROUTER_BASE}/sessions/${sessionId}/pipeline/tasks/${taskId}/${kind}${query ? `?${query}` : ""}`;
}

const NOT_SUPPORTED = "not supported by the current pipeline engine";

/**
 * Router API surface the Pipeline tab depends on — a dedicated interface
 * analogous to `RouterClient` (`ui/src/router/client.ts`) but scoped to
 * pipeline actions, per HANDOFF.md §4.
 */
export interface PipelineClient {
  listTasks(sessionId: string): Promise<Task[]>;
  startPipeline(
    sessionId: string,
    templateId: string,
    opts?: StartPipelineOptions,
  ): Promise<{ run_id: string }>;
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
  streamTaskOutput(sessionId: string, taskId: string, target?: TaskStreamTarget): PipelineOutputSource;
  streamTaskTurns(sessionId: string, taskId: string, target?: TaskStreamTarget): PipelineTurnsSource;
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
    opts?: StartPipelineOptions,
  ): Promise<{ run_id: string }> {
    return this.client.createPipelineRun(sessionId, templateId, opts?.excludeTaskIds);
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

  streamTaskOutput(sessionId: string, taskId: string, target?: TaskStreamTarget): PipelineOutputSource {
    const es = new EventSource(taskStreamUrl(sessionId, taskId, "output", target));
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

  streamTaskTurns(sessionId: string, taskId: string, target?: TaskStreamTarget): PipelineTurnsSource {
    const es = new EventSource(taskStreamUrl(sessionId, taskId, "turns", target));
    es.addEventListener("end", () => es.close());
    return {
      onTurns(listener: (turns: TranscriptMessage[]) => void): () => void {
        const handler = (ev: MessageEvent<string>) =>
          listener(JSON.parse(ev.data) as TranscriptMessage[]);
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

  // A long run's history is read in pages (getAllEvents). One page stopped
  // short of the run's end on a big run: the resumed fold then read
  // "running", and the live tail only starts after the page's last seq, so
  // nothing corrected it and Start stayed disabled.
  async getRunEvents(sessionId: string, since: number): Promise<Envelope[]> {
    return getAllEvents(this.client, { sessionId, since, types: FOLD_EVENT_TYPES });
  }
}

// Stateless — safe as a module-level singleton rather than re-created per render.
export const pipelineClient: PipelineClient = new FetchPipelineClient();
