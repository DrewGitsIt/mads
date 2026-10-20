import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { TasksTab } from "./TasksTab";
import { FakeRouterClient, fixtureSessions } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import {
  FakePipelineClient,
  pickedUpRunEnvelopes,
  PICKUP_RUN_SEQ,
  STOPPED_AT_SEQ,
  STOPPED_RUN_SEQ,
  stoppedRunEnvelopes,
  stoppedRunPickup,
  stoppedRunTasks,
} from "../pipeline/client/fixtures";
import { recordedSessionId, recordedTaskIds } from "../pipeline/client/recordedRun";
import type { Envelope, PipelineRun } from "../router/types";

// docs/PIPELINE-LEASES-DESIGN.md B3: Stop, interrupted runs, Interrupted
// tasks and pickup, as the Tasks tab shows them.

const STOPPED: PipelineRun = {
  session_id: recordedSessionId,
  run_seq: STOPPED_RUN_SEQ,
  run_id: "run_stopped",
  started_at: "2026-07-15T05:30:12Z",
  ended_at: "2026-07-15T05:40:12Z",
  outcome: "cancelled",
  tasks_done: 1,
  tasks_blocked: 0,
  tasks_skipped: 0,
  cost_usd: 0,
};
const PICKING_UP: PipelineRun = {
  ...STOPPED,
  run_seq: PICKUP_RUN_SEQ,
  run_id: "run_pickup",
  started_at: "2026-07-15T06:00:00Z",
  ended_at: undefined,
  outcome: "running",
};

function clients(runs: PipelineRun[], events: Envelope[]) {
  const tasks = stoppedRunTasks();
  const sessions = {
    ...fixtureSessions,
    [recordedSessionId]: { ...fixtureSessions["session-active"], id: recordedSessionId },
  };
  const routerClient = new FakeRouterClient(sessions, undefined, undefined, undefined, tasks);
  const pipelineClient = new FakePipelineClient(tasks.map((t) => ({ ...t })));
  pipelineClient.runs = runs;
  pipelineClient.runEvents = events;
  return { routerClient, pipelineClient };
}

const groupOf = async (label: RegExp) => (await screen.findByRole("heading", { name: label })).closest("section")!;

describe("TasksTab: Interrupted tasks", { timeout: 15_000 }, () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("Plan: an Interrupted task will run, says where it picks up, and is excludable", async () => {
    const { routerClient, pipelineClient } = clients([STOPPED], stoppedRunEnvelopes());
    pipelineClient.pickup = stoppedRunPickup;
    render(
      <TasksTab
        sessionId={recordedSessionId}
        routerClient={routerClient}
        pipelineClient={pipelineClient}
        focusRun={{ run: "new" }}
      />,
    );

    const willRun = await groupOf(/Will run/);
    await waitFor(() => expect(within(willRun).getByText("Interrupted · resumes at code_review")).toBeTruthy());
    expect(within(willRun).getByRole("button", { name: "flaky api" })).toBeTruthy();
    // flaky api and cli (which waits on it) both run.
    expect((await screen.findByRole("button", { name: /^Run \d+ tasks?$/ })).textContent).toBe("▶ Run 2 tasks");
    expect(pipelineClient.pickupCalls[0]).toEqual({ sessionId: recordedSessionId, templateId: "default-implementation" });

    fireEvent.click(screen.getByRole("checkbox", { name: "Include flaky api" }));
    expect((await screen.findByRole("button", { name: /^Run \d+ tasks?$/ })).textContent).toBe("▶ Run 0 tasks");
    expect(screen.getByText("cli won't run (depends on flaky api)")).toBeTruthy();
  });

  it("Plan: an in-progress task the pickup endpoint doesn't name is not in this run", async () => {
    const { routerClient, pipelineClient } = clients([STOPPED], stoppedRunEnvelopes());
    render(
      <TasksTab
        sessionId={recordedSessionId}
        routerClient={routerClient}
        pipelineClient={pipelineClient}
        focusRun={{ run: "new" }}
      />,
    );
    const out = await groupOf(/Not in this run/);
    await waitFor(() => expect(within(out).getByRole("button", { name: "flaky api" })).toBeTruthy());
    expect(within(out).getByText("won't run: already in progress")).toBeTruthy();
  });

  it("past: a stopped run reads stopped, and its in-flight task Interrupted with an editable sheet", async () => {
    const { routerClient, pipelineClient } = clients([STOPPED], stoppedRunEnvelopes());
    render(<TasksTab sessionId={recordedSessionId} routerClient={routerClient} pipelineClient={pipelineClient} />);

    expect(await screen.findByRole("button", { name: "New run" })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Run 1/ }).textContent).toContain("stopped");
    const group = await groupOf(/Interrupted/);
    expect(within(group).getByText("code_review · attempt 3")).toBeTruthy();

    // Nobody holds it: the detail sheet is editable.
    fireEvent.click(within(group).getByRole("button", { name: "Edit flaky api" }));
    const title = (await screen.findByLabelText("Title")) as HTMLInputElement;
    expect(title.readOnly).toBe(false);
    expect(screen.queryByText(/Locked while the pipeline works on it/)).toBeNull();
  });

  it("live: the picked-up task runs, is locked, and its lens marks the pickup", async () => {
    const { routerClient, pipelineClient } = clients([STOPPED, PICKING_UP], pickedUpRunEnvelopes());
    render(<TasksTab sessionId={recordedSessionId} routerClient={routerClient} pipelineClient={pipelineClient} />);

    const stop = await screen.findByRole("button", { name: "Stop" });
    const running = await groupOf(/Running/);
    expect(within(running).getByText("code_review · attempt 3")).toBeTruthy();

    // The live run holds it: read-only.
    fireEvent.click(within(running).getByRole("button", { name: "flaky api" }));
    const lens = await screen.findByTestId("task-lens");
    expect(within(lens).getByTestId("pickup-marker").textContent).toBe("Picked up from Run 1 · resumes at code_review");
    expect(within(lens).getByTestId("attempt-note").textContent).toContain("resumed from Run 1");
    // Stages Run 1 completed show done, marked with that run.
    expect(within(lens).getByRole("button", { name: "build: done in Run 1" })).toBeTruthy();

    fireEvent.click(within(lens).getByRole("button", { name: "Task details" }));
    const title = (await screen.findByLabelText("Title")) as HTMLInputElement;
    expect(title.readOnly).toBe(true);
    expect(screen.getByText(/Locked while the pipeline works on it/)).toBeTruthy();
    fireEvent.keyDown(document, { key: "Escape" });
    await waitFor(() => expect(screen.queryByLabelText("Title")).toBeNull());

    await waitFor(() => expect(stop.hasAttribute("disabled")).toBe(false));
    fireEvent.click(stop);
    await waitFor(() => expect(pipelineClient.cancelCalls).toEqual(["run_pickup"]));
  });

  it("past: a crashed run reads interrupted", async () => {
    const events = stoppedRunEnvelopes().map((e) =>
      e.seq === STOPPED_AT_SEQ + 1 ? { ...e, type: "pipeline.failed", payload: { run_id: "run_stopped", reason: "lost" } } : e,
    );
    const { routerClient, pipelineClient } = clients([{ ...STOPPED, outcome: "interrupted" }], events);
    render(<TasksTab sessionId={recordedSessionId} routerClient={routerClient} pipelineClient={pipelineClient} />);
    await screen.findByRole("button", { name: "New run" });
    expect(screen.getByRole("button", { name: /Run 1/ }).textContent).toContain("interrupted");
    expect(within(await groupOf(/Interrupted/)).getByRole("button", { name: "flaky api" })).toBeTruthy();
    expect(recordedTaskIds.flakyApi).toBeTruthy();
  });
});
