import { describe, expect, it } from "vitest";
import type { PipelineRun } from "../../router/types";
import { formatDuration, formatElapsed, formatRunWhen, outcomeLabel, outcomeTone, runOrdinal } from "./runBarFormat";

function run(seq: number, outcome: PipelineRun["outcome"], started_at: string, ended_at?: string): PipelineRun {
  return { session_id: "s", run_seq: seq, started_at, ended_at, outcome, tasks_done: 0, tasks_blocked: 0, tasks_skipped: 0, cost_usd: 0 };
}

describe("runBarFormat", () => {
  it("formats elapsed as m:ss, then h:mm:ss", () => {
    expect(formatElapsed(0)).toBe("0:00");
    expect(formatElapsed(-5000)).toBe("0:00");
    expect(formatElapsed(65_000)).toBe("1:05");
    expect(formatElapsed(31 * 60_000 + 6000)).toBe("31:06");
    expect(formatElapsed(3600_000 + 2 * 60_000 + 3000)).toBe("1:02:03");
  });

  it("formats a duration compactly", () => {
    expect(formatDuration(40_000)).toBe("40s");
    expect(formatDuration(22 * 60_000 + 30_000)).toBe("22m");
    expect(formatDuration(3600_000)).toBe("1h");
    expect(formatDuration(3900_000)).toBe("1h 5m");
  });

  it("says when a run happened", () => {
    const now = new Date("2026-10-19T12:14:30Z").getTime();
    expect(formatRunWhen(run(1, "running", "2026-10-19T12:14:00Z"), now)).toBe("started just now");
    expect(formatRunWhen(run(1, "running", "2026-10-19T12:00:00Z"), now)).toBe("started 14 min ago");
    expect(formatRunWhen(run(1, "running", "2026-10-19T09:00:00Z"), now)).toBe("started 3 h ago");
    expect(formatRunWhen(run(1, "completed", "2026-10-17T12:00:00Z", "2026-10-17T12:22:00Z"), now)).toBe("Oct 17 · 22m");
    expect(formatRunWhen(run(1, "interrupted", "2026-10-17T12:00:00Z"), now)).toBe("Oct 17");
  });

  it("numbers runs oldest-first by run_seq", () => {
    const runs = [run(90, "running", ""), run(40, "completed", ""), run(12, "failed", "")];
    expect(runOrdinal(runs, 12)).toBe(1);
    expect(runOrdinal(runs, 90)).toBe(3);
    expect(runOrdinal(runs, 120)).toBe(4);
  });

  it("maps outcomes to chip tones", () => {
    expect(outcomeTone("running")).toBe("info");
    expect(outcomeTone("completed")).toBe("ok");
    expect(outcomeTone("incomplete")).toBe("warn");
    expect(outcomeTone("interrupted")).toBe("warn");
    expect(outcomeTone("failed")).toBe("warn");
    expect(outcomeTone("cancelled")).toBe("neutral");
  });

  it("names a cancelled run stopped, and a failed or lost one interrupted", () => {
    expect(outcomeLabel("cancelled")).toBe("stopped");
    expect(outcomeLabel("failed")).toBe("interrupted");
    expect(outcomeLabel("interrupted")).toBe("interrupted");
    expect(outcomeLabel("incomplete")).toBe("incomplete");
  });
});
