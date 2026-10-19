import type { ChipTone } from "../../components/Chip/Chip";
import type { PipelineRun } from "../../router/types";

/** A run's clock: `m:ss`, or `h:mm:ss` past an hour. */
export function formatElapsed(ms: number): string {
  const totalSeconds = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(totalSeconds / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  const s = totalSeconds % 60;
  const ss = String(s).padStart(2, "0");
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${ss}` : `${m}:${ss}`;
}

/** A finished run's length for a menu row: `40s`, `22m`, `1h 5m`. */
export function formatDuration(ms: number): string {
  const totalSeconds = Math.max(0, Math.floor(ms / 1000));
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const totalMinutes = Math.floor(totalSeconds / 60);
  if (totalMinutes < 60) return `${totalMinutes}m`;
  const h = Math.floor(totalMinutes / 60);
  const m = totalMinutes % 60;
  return m ? `${h}h ${m}m` : `${h}h`;
}

/** Milliseconds from `startedAt` to `endedAt`, or to `now` when it hasn't ended. */
export function spanMs(startedAt: string, endedAt: string | undefined, now: number): number {
  const end = endedAt ? new Date(endedAt).getTime() : now;
  return end - new Date(startedAt).getTime();
}

/** The run menu's "when" column: `started 14 min ago` while running, else `Oct 17 · 22m`. */
export function formatRunWhen(run: PipelineRun, now: number): string {
  const started = new Date(run.started_at);
  if (run.outcome === "running") {
    const minutes = Math.floor((now - started.getTime()) / 60000);
    if (minutes < 1) return "started just now";
    if (minutes < 60) return `started ${minutes} min ago`;
    return `started ${Math.floor(minutes / 60)} h ago`;
  }
  const day = started.toLocaleDateString("en-US", { month: "short", day: "numeric" });
  return run.ended_at ? `${day} · ${formatDuration(spanMs(run.started_at, run.ended_at, now))}` : day;
}

/**
 * A run's number within its session, 1 being the oldest. Counted from
 * run_seq order rather than list position, so a live run the history
 * hasn't caught up with yet still gets the next number.
 */
export function runOrdinal(runs: PipelineRun[], runSeq: number): number {
  return runs.filter((r) => r.run_seq < runSeq).length + 1;
}

/**
 * A run outcome in the Tasks tab's words (docs/PIPELINE-LEASES-DESIGN.md
 * B1): a cancelled run was stopped by the user; a failed one (the engine
 * crashed) or one lost to a reboot ("interrupted" in the history) was
 * interrupted. Every task keeps its state either way.
 */
export function outcomeLabel(outcome: string): string {
  switch (outcome) {
    case "cancelled":
      return "stopped";
    case "failed":
    case "interrupted":
      return "interrupted";
    default:
      return outcome;
  }
}

export function outcomeTone(outcome: string | undefined): ChipTone {
  switch (outcome) {
    case "running":
      return "info";
    case "completed":
      return "ok";
    case "incomplete":
    case "failed":
    case "interrupted":
      return "warn";
    default:
      return "neutral";
  }
}
