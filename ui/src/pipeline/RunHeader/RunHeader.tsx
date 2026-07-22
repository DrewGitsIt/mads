import { useEffect, useReducer, useState } from "react";
import { Button } from "../../components/Button/Button";
import { Chip, type ChipTone } from "../../components/Chip/Chip";
import type { Task } from "../../router/types";
import type { PipelineClient } from "../client/pipelineClient";
import type { RunPhase } from "../state/usePipelineRun";
import styles from "./RunHeader.module.css";

const NOT_SUPPORTED = "not supported by the current pipeline engine";
const RUN_IN_PROGRESS = "a pipeline run is already in progress for this session";

export interface RunCounts {
  total: number;
  inProgress: number;
  done: number;
  blocked: number;
}

export interface RunHeaderProps {
  sessionId: string;
  client: PipelineClient;
  /** Defaults to the engine's only template today — see recordedRun.ts §5. */
  templateId?: string;
  phase: RunPhase;
  outcome?: string;
  runStartedAt?: string;
  runEndedAt?: string;
  tasks: Task[];
  /**
   * Overrides the tasks-derived chip counts with the displayed run's own
   * totals — needed when viewing a historical/interrupted run, whose task
   * counts must describe that run, not the session's current task list.
   */
  runCounts?: RunCounts;
  /** Fired once Start's `client.startPipeline` call resolves — the only place a run_id becomes known. */
  onRunStarted?: (runId: string) => void;
}

function formatElapsed(ms: number): string {
  const totalSeconds = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(totalSeconds / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  const s = totalSeconds % 60;
  const ss = String(s).padStart(2, "0");
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${ss}` : `${m}:${ss}`;
}

function outcomeTone(outcome?: string): ChipTone {
  switch (outcome) {
    case "completed":
      return "ok";
    case "failed":
      return "bad";
    case "incomplete":
    case "interrupted":
      return "warn";
    default:
      return "neutral";
  }
}

function countByStatus(tasks: Task[]) {
  let ready = 0;
  let inProgress = 0;
  let blocked = 0;
  let done = 0;
  for (const t of tasks) {
    if (t.status === "ready") ready++;
    else if (t.status === "in_progress") inProgress++;
    else if (t.status === "blocked") blocked++;
    else if (t.status === "done") done++;
  }
  return { ready, inProgress, blocked, done };
}

/**
 * Owns its own `run_id` locally, from `client.startPipeline`'s response —
 * `usePipelineRun` deliberately doesn't track one (no run id exists on any
 * envelope payload, HANDOFF.md §2). A remount mid-run (e.g. a page reload)
 * has no way to recover the in-flight run's id, so Cancel stays disabled
 * until this component itself starts a run.
 */
export function RunHeader({
  sessionId,
  client,
  templateId = "default-implementation",
  phase,
  outcome,
  runStartedAt,
  runEndedAt,
  tasks,
  runCounts,
  onRunStarted,
}: RunHeaderProps) {
  const [runId, setRunId] = useState<string | undefined>();
  const [starting, setStarting] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const [, forceTick] = useReducer((x: number) => x + 1, 0);

  useEffect(() => {
    if (phase !== "running") return;
    const id = setInterval(forceTick, 1000);
    return () => clearInterval(id);
  }, [phase]);

  async function handleStart() {
    setActionError(null);
    setStarting(true);
    try {
      const { run_id } = await client.startPipeline(sessionId, templateId);
      setRunId(run_id);
      onRunStarted?.(run_id);
    } catch (err) {
      setActionError(err instanceof Error ? err.message : String(err));
    } finally {
      setStarting(false);
    }
  }

  async function handleCancel() {
    if (!runId) return;
    setActionError(null);
    setCancelling(true);
    try {
      await client.cancelPipeline(runId);
    } catch (err) {
      setActionError(err instanceof Error ? err.message : String(err));
    } finally {
      setCancelling(false);
    }
  }

  const statusCounts = countByStatus(tasks);
  const counts: RunCounts = runCounts ?? {
    total: tasks.length,
    inProgress: statusCounts.inProgress,
    done: statusCounts.done,
    blocked: statusCounts.blocked,
  };
  const elapsedMs = runStartedAt
    ? (phase === "running" ? Date.now() : runEndedAt ? new Date(runEndedAt).getTime() : Date.now()) -
      new Date(runStartedAt).getTime()
    : undefined;

  return (
    <div className={styles.header}>
      <div className={styles.row}>
        <Chip tone={phase === "running" ? "info" : outcomeTone(outcome)}>
          {phase === "running" ? "running" : phase === "finished" ? outcome ?? "finished" : "idle"}
        </Chip>

        {elapsedMs !== undefined && <span className={styles.elapsed}>{formatElapsed(elapsedMs)}</span>}

        <div className={styles.spacer} />

        <div className={styles.counts}>
          <Chip tone="neutral">{counts.total} tasks</Chip>
          <Chip tone="info">{counts.inProgress} in progress</Chip>
          <Chip tone="ok">{counts.done} done</Chip>
          <Chip tone="bad">{counts.blocked} blocked</Chip>
        </div>

        <div className={styles.actions}>
          <Button
            variant="primary"
            size="sm"
            disabled={phase === "running" || starting}
            title={phase === "running" ? RUN_IN_PROGRESS : undefined}
            onClick={() => void handleStart()}
          >
            Start
          </Button>
          <Button variant="secondary" size="sm" disabled title={NOT_SUPPORTED}>
            Pause
          </Button>
          <Button variant="secondary" size="sm" disabled title={NOT_SUPPORTED}>
            Resume
          </Button>
          <Button
            variant="secondary"
            size="sm"
            disabled={phase !== "running" || !runId || cancelling}
            onClick={() => void handleCancel()}
          >
            Cancel
          </Button>
        </div>
      </div>

      {actionError && <div className={styles.error}>{actionError}</div>}
    </div>
  );
}
