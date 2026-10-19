import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { TasksTab } from "./TasksTab";
import { FakeRouterClient, fixtureSessions } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import { FakePipelineClient } from "../pipeline/client/fixtures";
import {
  recordedEnvelopes,
  recordedSessionId,
  recordedTaskIds,
  recordedTasks,
} from "../pipeline/client/recordedRun";
import type { Envelope, PipelineRun, Task } from "../router/types";

function routerWith(tasks: Task[]) {
  const sessions = {
    ...fixtureSessions,
    [recordedSessionId]: { ...fixtureSessions["session-active"], id: recordedSessionId },
  };
  return new FakeRouterClient(sessions, undefined, undefined, undefined, tasks);
}

// Two ready tasks, the second depending on the first.
const CHAIN: Task[] = [
  { ...recordedTasks[0], id: "a", title: "core", status: "ready", dependencies: [] },
  { ...recordedTasks[0], id: "b", title: "api", status: "ready", dependencies: ["a"] },
];

const blockedRow = async (title: string) => {
  const heading = await screen.findByRole("heading", { name: /Blocked/ });
  return within(heading.closest("section")!).getByRole("button", { name: title });
};

const FINISHED: PipelineRun = {
  session_id: recordedSessionId,
  run_seq: 7,
  started_at: "2026-07-15T05:30:12Z",
  ended_at: "2026-07-15T05:40:12Z",
  outcome: "incomplete",
  tasks_done: 1,
  tasks_blocked: 1,
  tasks_skipped: 1,
  cost_usd: 0,
};

// The recorded run up to (not including) its pipeline.completed: still live.
const completedAt = recordedEnvelopes.findIndex((e) => e.type === "pipeline.completed");
const UNFINISHED = recordedEnvelopes.slice(0, completedAt);

function withRunId(envs: Envelope[], runId: string): Envelope[] {
  return envs.map((e) =>
    e.type === "pipeline.started" ? { ...e, payload: { ...(e.payload as object), run_id: runId } } : e,
  );
}

function recordedClients({ runs = [], events = [] }: { runs?: PipelineRun[]; events?: Envelope[] } = {}) {
  const pipelineClient = new FakePipelineClient(recordedTasks.map((t) => ({ ...t })));
  pipelineClient.runs = runs;
  pipelineClient.runEvents = events;
  return { routerClient: routerWith(recordedTasks.map((t) => ({ ...t }))), pipelineClient };
}

const runButton = () => screen.findByRole("button", { name: /^Run \d+ tasks?$/ });

// Each test mounts the whole tab (metro, list, run fold, lens); under a full
// parallel run that can outlast the default 5s.
describe("TasksTab", { timeout: 15_000 }, () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    // useCapabilities() hits the singleton router (real fetch), independent
    // of the routerClient prop these tests otherwise fully control.
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  describe("Plan (no runs yet)", () => {
    it("shows the metro and the plan list, and Run counts the ready tasks", async () => {
      const client = new FakeRouterClient();
      render(<TasksTab sessionId="session-active" routerClient={client} pipelineClient={new FakePipelineClient()} />);
      await waitFor(() => expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy());
      expect(screen.getByTestId("metro")).toBeTruthy();
      expect(await runButton()).toBeTruthy();
      expect(screen.getByRole("heading", { name: /Will run/ })).toBeTruthy();
    });

    it("toggles to the graph view and back", async () => {
      const client = new FakeRouterClient();
      render(<TasksTab sessionId="session-active" routerClient={client} pipelineClient={new FakePipelineClient()} />);
      await waitFor(() => expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy());

      fireEvent.click(screen.getByRole("radio", { name: "Graph" }));
      await waitFor(() => expect(screen.getByTestId("rf__wrapper")).toBeTruthy());

      fireEvent.click(screen.getByRole("radio", { name: "List" }));
      expect(screen.queryByTestId("rf__wrapper")).toBeNull();
    });

    it("opens the detail sheet on a row click, editable and closable", async () => {
      const client = new FakeRouterClient();
      render(<TasksTab sessionId="session-active" routerClient={client} pipelineClient={new FakePipelineClient()} />);
      await waitFor(() => expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy());

      fireEvent.click(screen.getByText("Add retry backoff to the ingest worker"));
      const titleInput = (await screen.findByLabelText("Title")) as HTMLInputElement;
      expect(titleInput.value).toBe("Add retry backoff to the ingest worker");
      expect(screen.queryByTestId("task-lens")).toBeNull();

      fireEvent.click(screen.getByRole("button", { name: "Close" }));
      expect(screen.queryByLabelText("Title")).toBeNull();
    });

    it("keeps Bulk import beside + Add task, mutually exclusive with the detail sheet", async () => {
      const client = new FakeRouterClient();
      render(<TasksTab sessionId="session-active" routerClient={client} pipelineClient={new FakePipelineClient()} />);
      await waitFor(() => expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy());

      fireEvent.click(screen.getByRole("button", { name: "Bulk import" }));
      expect(await screen.findByText("Bulk import tasks")).toBeTruthy();

      fireEvent.click(screen.getByText("Add retry backoff to the ingest worker"));
      expect(await screen.findByLabelText("Title")).toBeTruthy();
      expect(screen.queryByText("Bulk import tasks")).toBeNull();
    });

    it("+ Add task creates a draft and opens it", async () => {
      const client = new FakeRouterClient();
      const create = vi.spyOn(client, "createTask");
      render(<TasksTab sessionId="session-active" routerClient={client} pipelineClient={new FakePipelineClient()} />);
      await waitFor(() => expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy());

      fireEvent.click(screen.getByRole("button", { name: /Add task/ }));
      const titleInput = (await screen.findByLabelText("Title")) as HTMLInputElement;
      expect(titleInput.value).toBe("New task");
      expect(create).toHaveBeenCalledWith(
        expect.objectContaining({ session_id: "session-active", draft: true, source: "user" }),
      );
    });

    it("starts the run with the unticked tasks left out, then follows it live", async () => {
      const pipelineClient = new FakePipelineClient(CHAIN);
      render(<TasksTab sessionId={recordedSessionId} routerClient={routerWith(CHAIN)} pipelineClient={pipelineClient} />);
      expect((await runButton()).textContent).toBe("▶ Run 2 tasks");

      fireEvent.click(screen.getByRole("checkbox", { name: "Include api" }));
      expect((await runButton()).textContent).toBe("▶ Run 1 task");

      fireEvent.click(await runButton());
      await waitFor(() => expect(pipelineClient.startCalls).toHaveLength(1));
      expect(pipelineClient.startCalls[0]).toMatchObject({
        sessionId: recordedSessionId,
        templateId: "default-implementation",
        opts: { excludeTaskIds: ["b"] },
      });

      // pipeline.started lands: the bar switches to the live run.
      const source = FakeEventSource.instances.find((s) => s.url.includes("pipeline.started"))!;
      act(() => {
        const started = recordedEnvelopes.find((e) => e.type === "pipeline.started")!;
        source.emit(started.type, JSON.stringify(started));
      });
      expect(await screen.findByRole("button", { name: "Stop" })).toBeTruthy();
    });

    it("names the tasks a left-out dependency keeps out", async () => {
      const pipelineClient = new FakePipelineClient(CHAIN);
      render(<TasksTab sessionId={recordedSessionId} routerClient={routerWith(CHAIN)} pipelineClient={pipelineClient} />);
      await runButton();
      fireEvent.click(screen.getByRole("checkbox", { name: "Include core" }));
      expect(await screen.findByText("api won't run (depends on core)")).toBeTruthy();
    });
  });

  describe("a past run", () => {
    it("is the default when the session has one, and New run goes to Plan", async () => {
      const { routerClient, pipelineClient } = recordedClients({ runs: [FINISHED], events: recordedEnvelopes });
      render(<TasksTab sessionId={recordedSessionId} routerClient={routerClient} pipelineClient={pipelineClient} />);

      expect(await screen.findByRole("button", { name: "New run" })).toBeTruthy();
      expect(screen.getByRole("button", { name: /Run 1/ }).textContent).toContain("incomplete");

      fireEvent.click(screen.getByRole("button", { name: "New run" }));
      expect(await runButton()).toBeTruthy();
    });

    it("zooms into a task: the lens replaces the list under the metro, Esc closes the sheet before the lens", async () => {
      const { routerClient, pipelineClient } = recordedClients({ runs: [FINISHED], events: recordedEnvelopes });
      render(<TasksTab sessionId={recordedSessionId} routerClient={routerClient} pipelineClient={pipelineClient} />);
      await screen.findByRole("button", { name: "New run" });

      fireEvent.click(await blockedRow("flaky api"));

      const lens = await screen.findByTestId("task-lens");
      expect(screen.getByTestId("metro")).toBeTruthy();
      expect(screen.queryByRole("heading", { name: /Blocked/ })).toBeNull();
      // The run fold reached the lens: the blocking stage is named.
      expect(within(lens).getByText(/Blocked at code_review/)).toBeTruthy();

      fireEvent.click(within(lens).getByRole("button", { name: "Edit & retry" }));
      expect(await screen.findByText("Why it's blocked")).toBeTruthy();
      expect(screen.getByRole("button", { name: "Save & retry" })).toBeTruthy();

      fireEvent.keyDown(document, { key: "Escape" });
      await waitFor(() => expect(screen.queryByText("Why it's blocked")).toBeNull());
      expect(screen.getByTestId("task-lens")).toBeTruthy();

      fireEvent.keyDown(document, { key: "Escape" });
      await waitFor(() => expect(screen.queryByTestId("task-lens")).toBeNull());
      expect(screen.getByRole("heading", { name: /Blocked/ })).toBeTruthy();
    });

    it("Save & retry saves, then retries the task", async () => {
      const { routerClient, pipelineClient } = recordedClients({ runs: [FINISHED], events: recordedEnvelopes });
      const retry = vi.spyOn(pipelineClient, "retryTask");
      render(<TasksTab sessionId={recordedSessionId} routerClient={routerClient} pipelineClient={pipelineClient} />);
      await screen.findByRole("button", { name: "New run" });

      fireEvent.click(await blockedRow("flaky api"));
      fireEvent.click(await screen.findByRole("button", { name: "Edit & retry" }));
      fireEvent.click(await screen.findByRole("button", { name: "Save & retry" }));
      await waitFor(() => expect(retry).toHaveBeenCalledWith(recordedTaskIds.flakyApi));
      await waitFor(() => expect(screen.queryByText("Why it's blocked")).toBeNull());
    });

    it("focusRun picks Plan even when there is a past run", async () => {
      const { routerClient, pipelineClient } = recordedClients({ runs: [FINISHED], events: recordedEnvelopes });
      render(
        <TasksTab
          sessionId={recordedSessionId}
          routerClient={routerClient}
          pipelineClient={pipelineClient}
          focusRun={{ run: "new" }}
        />,
      );
      expect(await runButton()).toBeTruthy();
    });
  });

  describe("a live run", () => {
    it("is the default while one runs; Stop explains itself when the run has no id", async () => {
      const { routerClient, pipelineClient } = recordedClients({
        runs: [{ ...FINISHED, outcome: "running", ended_at: undefined }],
        events: UNFINISHED,
      });
      render(<TasksTab sessionId={recordedSessionId} routerClient={routerClient} pipelineClient={pipelineClient} />);

      const cancel = await screen.findByRole("button", { name: "Stop" });
      expect(cancel.hasAttribute("disabled")).toBe(true);
      expect(cancel.getAttribute("title")).toMatch(/can't be stopped from here/);
    });

    it("stops by the run id from the fold, so it works after a reload", async () => {
      const { routerClient, pipelineClient } = recordedClients({
        runs: [{ ...FINISHED, outcome: "running", ended_at: undefined }],
        events: withRunId(UNFINISHED, "run_42"),
      });
      const cancel = vi.spyOn(pipelineClient, "cancelPipeline");
      render(<TasksTab sessionId={recordedSessionId} routerClient={routerClient} pipelineClient={pipelineClient} />);

      const button = await screen.findByRole("button", { name: "Stop" });
      await waitFor(() => expect(button.hasAttribute("disabled")).toBe(false));
      fireEvent.click(button);
      await waitFor(() => expect(cancel).toHaveBeenCalledWith("run_42"));
    });

    it("stays on the run, as past, once it finishes", async () => {
      const { routerClient, pipelineClient } = recordedClients({
        runs: [{ ...FINISHED, outcome: "running", ended_at: undefined }],
        events: UNFINISHED,
      });
      render(<TasksTab sessionId={recordedSessionId} routerClient={routerClient} pipelineClient={pipelineClient} />);
      await screen.findByRole("button", { name: "Stop" });

      const source = FakeEventSource.instances.find((s) => s.url.includes("pipeline.completed"))!;
      act(() => {
        const done = recordedEnvelopes[completedAt];
        source.emit(done.type, JSON.stringify(done));
      });
      expect(await screen.findByRole("button", { name: "New run" })).toBeTruthy();
      expect(screen.getByRole("button", { name: /Run 1/ }).textContent).toContain("incomplete");
    });
  });
});
