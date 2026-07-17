import { act } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { PipelineTab } from "./PipelineTab";
import { FakeEventSource } from "../router/testFakeEventSource";
import { FakePipelineClient } from "./client/fixtures";
import { recordedEnvelopes, recordedSessionId, recordedTasks } from "./client/recordedRun";
import type { Envelope, PipelineRun } from "../router/types";

function makeRun(overrides: Partial<PipelineRun>): PipelineRun {
  return {
    session_id: recordedSessionId,
    run_seq: 1,
    started_at: "2026-07-15T05:30:12.265401682Z",
    ended_at: "2026-07-15T05:30:12.28113183Z",
    outcome: "completed",
    tasks_done: 0,
    tasks_blocked: 0,
    tasks_skipped: 0,
    cost_usd: 0,
    ...overrides,
  };
}

const run2TaskId = "tsk_run2_only";

const run2Envelopes: Envelope[] = [
  {
    seq: 100,
    timestamp: "2026-07-16T05:00:00Z",
    type: "pipeline.started",
    session_id: recordedSessionId,
    actor: "pipeline",
    payload: { template: "default-implementation" },
  },
  {
    seq: 101,
    timestamp: "2026-07-16T05:00:01Z",
    type: "stage.started",
    session_id: recordedSessionId,
    task_id: run2TaskId,
    actor: "pipeline",
    payload: { stage: "architect" },
  },
  {
    seq: 102,
    timestamp: "2026-07-16T05:00:02Z",
    type: "stage.completed",
    session_id: recordedSessionId,
    task_id: run2TaskId,
    actor: "pipeline",
    payload: { stage: "architect" },
  },
  {
    seq: 103,
    timestamp: "2026-07-16T05:00:03Z",
    type: "pipeline.completed",
    session_id: recordedSessionId,
    actor: "pipeline",
    payload: { outcome: "completed", done: 1, blocked: 0, skipped: 0 },
  },
];

function playRecordedRun() {
  const source = FakeEventSource.instances[0];
  act(() => {
    for (const env of recordedEnvelopes) {
      source.emit(env.type, JSON.stringify(env));
    }
  });
}

describe("PipelineTab", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("composes the run header, board, and blocked panel from the recorded run", async () => {
    const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
    render(<PipelineTab sessionId={recordedSessionId} pipelineClient={client} />);

    await waitFor(() => expect(screen.getByRole("button", { name: "Start" })).toBeTruthy());
    playRecordedRun();

    await waitFor(() => expect(screen.getByText("incomplete")).toBeTruthy());
    expect(screen.getByText("Done")).toBeTruthy();
    // RunHeader's count chip and BlockedPanel's heading chip both say "1 blocked".
    expect(screen.getAllByText("1 blocked").length).toBe(2);
    // core lib appears once, in Done — flaky api is excluded from the board
    // columns and appears only in BlockedPanel.
    expect(screen.getAllByText("core lib").length).toBe(1);
  });

  it("opens the drill-in when a task card is clicked, and closes it", async () => {
    const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
    render(<PipelineTab sessionId={recordedSessionId} pipelineClient={client} />);

    await waitFor(() => expect(screen.getByRole("button", { name: "Start" })).toBeTruthy());
    playRecordedRun();
    await waitFor(() => expect(screen.getByText("incomplete")).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: "core lib" }));
    await waitFor(() =>
      expect(document.querySelector('[data-testid="terminal-view"]')).toBeTruthy(),
    );

    fireEvent.click(screen.getByRole("button", { name: "close" }));
    expect(document.querySelector('[data-testid="terminal-view"]')).toBeNull();
  });

  it("opens the drill-in from a blocked task's card and shows its feedback", async () => {
    const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
    render(<PipelineTab sessionId={recordedSessionId} pipelineClient={client} />);

    await waitFor(() => expect(screen.getByRole("button", { name: "Start" })).toBeTruthy());
    playRecordedRun();
    await waitFor(() => expect(screen.getByText("incomplete")).toBeTruthy());

    fireEvent.click(screen.getByText("flaky api").closest("button")!);
    await waitFor(() =>
      expect(document.querySelector('[data-testid="terminal-view"]')).toBeTruthy(),
    );
    expect(screen.getAllByText("code_review").length).toBeGreaterThan(0);
  });

  it("renders a historical run's final board with no live SSE playback", async () => {
    const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
    client.runs = [makeRun({ run_seq: 7, outcome: "incomplete", tasks_done: 1, tasks_blocked: 1 })];
    client.runEvents = recordedEnvelopes;
    render(<PipelineTab sessionId={recordedSessionId} pipelineClient={client} />);

    await waitFor(() => expect(screen.getByText("incomplete")).toBeTruthy());
    expect(screen.queryByText("idle")).toBeNull();
    expect(screen.getByText("Done")).toBeTruthy();
    expect(screen.getAllByText("1 blocked").length).toBe(2);
    expect(screen.getByRole("combobox", { name: "Pipeline run" })).toBeTruthy();
  });

  it("switches folded runs via the run picker", async () => {
    const client = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
    client.runs = [
      makeRun({ run_seq: 7, outcome: "incomplete", tasks_done: 1, tasks_blocked: 1 }),
      makeRun({ run_seq: 100, outcome: "completed", started_at: "2026-07-16T05:00:00Z", ended_at: "2026-07-16T05:00:03Z", tasks_done: 1 }),
    ];
    client.runEvents = [...recordedEnvelopes, ...run2Envelopes];
    render(<PipelineTab sessionId={recordedSessionId} pipelineClient={client} />);

    // Newest terminal run (100) is selected by default — its fold only ever
    // reached the "architect" stage, so no "executor" column exists yet.
    await waitFor(() => expect(screen.getByText("architect")).toBeTruthy());
    expect(screen.queryByText("executor")).toBeNull();

    fireEvent.change(screen.getByRole("combobox", { name: "Pipeline run" }), {
      target: { value: "7" },
    });

    // Run 7 (the full recorded run) folds all six stages, including executor.
    await waitFor(() => expect(screen.getByText("executor")).toBeTruthy());
  });
});
