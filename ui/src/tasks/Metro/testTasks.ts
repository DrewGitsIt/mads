import type { Task } from "../../router/types";
import type { RunStage } from "../../pipeline/plan/runStages";
import type { StageAttempt, TaskRunState } from "../../pipeline/state/usePipelineRun";

// Test builders shared by the metro and run-list tests.

export function task(id: string, over: Partial<Task> = {}): Task {
  return {
    id,
    session_id: "s1",
    status: "ready",
    retry_count: 0,
    created_at: "2026-10-19T00:00:00Z",
    updated_at: "2026-10-19T00:00:00Z",
    title: `Task ${id}`,
    source: "user",
    ...over,
  };
}

export function byId(tasks: Task[]): Record<string, Task> {
  return Object.fromEntries(tasks.map((t) => [t.id, t]));
}

export function attempt(stage: string, n: number, status: StageAttempt["status"], reason?: string): StageAttempt {
  return { stage, attempt: n, status, reason, startedSeq: n };
}

export function runState(taskId: string, attempts: StageAttempt[], currentStage?: string): TaskRunState {
  return { taskId, attempts, currentStage, touchedFiles: [] };
}

export const DEFAULT_STAGES: RunStage[] = [
  { name: "architect", kind: "agent" },
  { name: "executor", kind: "agent" },
  { name: "build", kind: "command", loopBack: "executor", maxLoops: 2 },
  { name: "code_review", kind: "agent", loopBack: "executor", maxLoops: 2, then: "block" },
  { name: "merge", kind: "command" },
  { name: "verify", kind: "command" },
];
