import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { SessionWorkspace, openTasksTitle } from "./SessionWorkspace";
import { FakeRouterClient } from "../../../router/fixtures";
import { FakePipelineClient } from "../../../pipeline/client/fixtures";
import { FakeEventSource } from "../../../router/testFakeEventSource";
import { makeFakeShellHost } from "../../../conversation/ShellDock/fakeShellHost";
import { resetShellDockCache } from "../../../conversation/ShellDock/dockState";
import { task } from "../../../tasks/Metro/testTasks";
import type { Envelope } from "../../../router/types";

const SESSION_ID = "session-active";

function renderWorkspace(props: Partial<Parameters<typeof SessionWorkspace>[0]> = {}) {
  return render(
    <SessionWorkspace
      sessionId={SESSION_ID}
      routerClient={new FakeRouterClient()}
      pipelineClient={new FakePipelineClient()}
      onSleep={vi.fn()}
      onWake={vi.fn()}
      onClose={vi.fn()}
      {...props}
    />,
  );
}

describe("SessionWorkspace", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    delete (window as { mads?: unknown }).mads;
    window.localStorage.clear();
    resetShellDockCache();
  });

  it("defaults to the Conversation tab, lazy-mounting the other two", async () => {
    renderWorkspace();

    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "Conversation" }).getAttribute("aria-selected")).toBe(
        "true",
      ),
    );
    expect(screen.getByRole("tab", { name: "Tasks" }).getAttribute("aria-selected")).toBe("false");
    expect(screen.getByRole("tab", { name: "Changes" }).getAttribute("aria-selected")).toBe("false");
    expect(screen.queryByRole("tab", { name: "Pipeline" })).toBeNull();

    expect(screen.queryByRole("tabpanel", { name: "Tasks" })).toBeNull();
    expect(screen.queryByRole("tabpanel", { name: "Changes" })).toBeNull();
  });

  // The title stands alone: no "ACTIVE"/"INACTIVE" chip beside it — the rail
  // card's status line says what the session is doing.
  it("shows the session name with no activity chip beside it", async () => {
    renderWorkspace();

    const nameButton = await screen.findByTitle("Rename session");
    await waitFor(() => expect(nameButton.textContent).toBe("Add retry backoff to the ingest worker"));
    const titleRow = nameButton.parentElement!;
    expect(titleRow.textContent).toBe("Add retry backoff to the ingest worker");
    expect(within(titleRow.parentElement!).queryByText(/^(active|inactive)$/i)).toBeNull();
  });

  it("renames the session from the header via renameSession", async () => {
    const routerClient = new FakeRouterClient();
    const rename = vi.spyOn(routerClient, "renameSession");
    render(
      <SessionWorkspace
        sessionId={SESSION_ID}
        routerClient={routerClient}
        pipelineClient={new FakePipelineClient()}
          onSleep={vi.fn()}
          onWake={vi.fn()}
          onClose={vi.fn()}
      />,
    );

    const nameButton = await screen.findByTitle("Rename session");
    fireEvent.click(nameButton);
    const input = screen.getByLabelText("Session name") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "Renamed via header" } });
    fireEvent.keyDown(input, { key: "Enter" });

    expect(rename).toHaveBeenCalledWith(SESSION_ID, "Renamed via header");
    await waitFor(() => expect(screen.getByTitle("Rename session").textContent).toBe("Renamed via header"));
  });

  it("preserves a visited tab's state (Tasks' Graph view) across a tab flip", async () => {
    renderWorkspace();
    await waitFor(() => expect(screen.getByRole("tab", { name: "Conversation" })).toBeTruthy());

    fireEvent.click(screen.getByRole("tab", { name: "Tasks" }));
    const graph = await screen.findByRole("radio", { name: "Graph" });

    fireEvent.click(graph);
    await waitFor(() => expect(screen.getByRole("radio", { name: "Graph" }).getAttribute("aria-checked")).toBe("true"));

    fireEvent.click(screen.getByRole("tab", { name: "Conversation" }));
    fireEvent.click(screen.getByRole("tab", { name: "Tasks" }));

    expect(screen.getByRole("radio", { name: "Graph" }).getAttribute("aria-checked")).toBe("true");
  });

  it("shows the open-task-count badge on the Tasks tab, hidden at 0", async () => {
    const { rerender } = renderWorkspace({ openTaskBreakdown: { running: 3, ready: 0, blocked: 0 } });
    await waitFor(() => expect(screen.getByRole("tab", { name: "Tasks" })).toBeTruthy());
    expect(screen.getByRole("tab", { name: "Tasks" }).textContent).toContain("3");

    rerender(
      <SessionWorkspace
        sessionId={SESSION_ID}
        routerClient={new FakeRouterClient()}
        pipelineClient={new FakePipelineClient()}
          onSleep={vi.fn()}
          onWake={vi.fn()}
          onClose={vi.fn()}
      />,
    );
    expect(screen.getByRole("tab", { name: "Tasks" }).textContent).toBe("Tasks");
  });

  it("swaps the Tasks badge for a spinner and the run's own running count while a run is in flight", async () => {
    // Two tasks in progress: one the run claimed, one a person set in
    // progress themselves (the run never touches it). A third is ready.
    const tasks = [
      task("claimed", { session_id: SESSION_ID, status: "in_progress" }),
      task("mine", { session_id: SESSION_ID, status: "in_progress" }),
      task("next", { session_id: SESSION_ID }),
    ];
    const pipelineClient = new FakePipelineClient(tasks);
    const env = (seq: number, type: string, extra: Partial<Envelope> = {}): Envelope => ({
      seq,
      timestamp: "2026-10-19T10:00:00Z",
      type,
      session_id: SESSION_ID,
      actor: "pipeline",
      ...extra,
    });
    const claim = (seq: number, taskId: string) =>
      env(seq, "task.status_changed", { task_id: taskId, payload: { from: "ready", to: "in_progress" } });
    pipelineClient.runs = [
      {
        session_id: SESSION_ID,
        run_seq: 10,
        run_id: "r1",
        started_at: "2026-10-19T10:00:00Z",
        outcome: "running",
        tasks_done: 0,
        tasks_blocked: 0,
        tasks_skipped: 0,
        cost_usd: 0,
      },
    ];
    pipelineClient.runEvents = [env(10, "pipeline.started", { payload: { run_id: "r1" } }), claim(11, "claimed")];

    const routerClient = new FakeRouterClient(undefined, undefined, undefined, undefined, tasks);
    renderWorkspace({ routerClient, pipelineClient, openTaskBreakdown: { running: 2, ready: 3, blocked: 0 } });
    const tasksTab = await screen.findByRole("tab", { name: "Tasks" });
    expect(tasksTab.textContent).toBe("Tasks5");

    const source = FakeEventSource.instances.find((s) => s.url.includes("pipeline.started"));
    act(() => {
      source?.emit("pipeline.started", JSON.stringify(env(10, "pipeline.started", { payload: { run_id: "r1" } })));
    });
    await waitFor(() => expect(within(tasksTab).getByTestId("tasks-run-badge").textContent).toBe("1"));
    expect(within(tasksTab).getByTestId("tasks-run-badge").getAttribute("title")).toBe(
      "Pipeline running · 1 task running",
    );

    // The run claims the next task: it counts once the task list says so.
    tasks[2].status = "in_progress";
    act(() => {
      source?.emit("task.status_changed", JSON.stringify(claim(12, "next")));
    });
    await waitFor(() => expect(within(tasksTab).getByTestId("tasks-run-badge").textContent).toBe("2"));

    act(() => {
      source?.emit(
        "pipeline.completed",
        JSON.stringify({ seq: 13, type: "pipeline.completed", actor: "user", session_id: SESSION_ID }),
      );
    });
    await waitFor(() => expect(within(tasksTab).queryByTestId("tasks-run-badge")).toBeNull());
    expect(tasksTab.textContent).toBe("Tasks5");
  });

  it("opens the retired pipeline tab id as Tasks", async () => {
    renderWorkspace({ initialTab: "pipeline" });
    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "Tasks" }).getAttribute("aria-selected")).toBe("true"),
    );
  });

  it("openRun surfaces the Tasks tab on Plan", async () => {
    const { rerender } = renderWorkspace();
    await screen.findByRole("tab", { name: "Conversation" });
    const props = {
      sessionId: SESSION_ID,
      routerClient: new FakeRouterClient(),
      pipelineClient: new FakePipelineClient(),
      onSleep: vi.fn(),
      onWake: vi.fn(),
      onClose: vi.fn(),
    };
    rerender(<SessionWorkspace {...props} openRun={{ run: "new" }} />);
    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "Tasks" }).getAttribute("aria-selected")).toBe("true"),
    );
    expect(await screen.findByRole("button", { name: /Run \d+ tasks?/ })).toBeTruthy();
  });

  it("folds Shell and Restart Agent into the single workspace kebab; Restart bounces the agent", async () => {
    const routerClient = new FakeRouterClient();
    const bounce = vi.spyOn(routerClient, "bounce");
    render(
      <SessionWorkspace
        sessionId={SESSION_ID}
        routerClient={routerClient}
        pipelineClient={new FakePipelineClient()}
          onSleep={vi.fn()}
          onWake={vi.fn()}
          onClose={vi.fn()}
      />,
    );
    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());

    fireEvent.click(screen.getByLabelText("Workspace actions"));
    expect(screen.getByRole("button", { name: "Open shell" })).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Restart Agent" }));
    expect(bounce).toHaveBeenCalledWith(SESSION_ID);
  });

  it("Restart Agent shows a pending state while the bounce is in flight, and clears it on completion", async () => {
    const routerClient = new FakeRouterClient();
    let resolveBounce!: () => void;
    vi.spyOn(routerClient, "bounce").mockReturnValue(
      new Promise<void>((resolve) => {
        resolveBounce = resolve;
      }),
    );
    render(
      <SessionWorkspace
        sessionId={SESSION_ID}
        routerClient={routerClient}
        pipelineClient={new FakePipelineClient()}
          onSleep={vi.fn()}
          onWake={vi.fn()}
          onClose={vi.fn()}
      />,
    );
    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());

    fireEvent.click(screen.getByLabelText("Workspace actions"));
    fireEvent.click(screen.getByRole("button", { name: "Restart Agent" }));

    fireEvent.click(screen.getByLabelText("Workspace actions"));
    const pending = screen.getByRole("button", { name: /Restarting Agent/ }) as HTMLButtonElement;
    expect(pending.disabled).toBe(true);

    await act(async () => {
      resolveBounce();
      await Promise.resolve();
    });

    // Menu is still open (restarting didn't close it) — the item is back
    // to its normal, enabled state.
    expect(screen.getByRole("button", { name: "Restart Agent" }).hasAttribute("disabled")).toBe(
      false,
    );
  });

  it("Restart Agent failure surfaces in the action-error toast rather than being swallowed", async () => {
    const routerClient = new FakeRouterClient();
    vi.spyOn(routerClient, "bounce").mockRejectedValue(new Error("child busy past terminate grace"));
    render(
      <SessionWorkspace
        sessionId={SESSION_ID}
        routerClient={routerClient}
        pipelineClient={new FakePipelineClient()}
          onSleep={vi.fn()}
          onWake={vi.fn()}
          onClose={vi.fn()}
      />,
    );
    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());

    fireEvent.click(screen.getByLabelText("Workspace actions"));
    fireEvent.click(screen.getByRole("button", { name: "Restart Agent" }));

    await waitFor(() => expect(screen.getByText("Session action failed")).toBeTruthy());
    expect(screen.getByText("child busy past terminate grace")).toBeTruthy();
  });

  it("Restart Agent success refetches the session and re-attaches the stream, even though the cached record still says inactive", async () => {
    const routerClient = new FakeRouterClient();
    const bounce = vi.spyOn(routerClient, "bounce").mockResolvedValue(undefined);
    const getSession = vi.spyOn(routerClient, "getSession");
    render(
      <SessionWorkspace
        sessionId="session-inactive"
        routerClient={routerClient}
        pipelineClient={new FakePipelineClient()}
          onSleep={vi.fn()}
          onWake={vi.fn()}
          onClose={vi.fn()}
      />,
    );
    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());
    const sessionCallsBefore = getSession.mock.calls.length;
    const streamCountBefore = routerClient.sessionEventSources.length;

    fireEvent.click(screen.getByLabelText("Workspace actions"));
    fireEvent.click(screen.getByRole("button", { name: "Restart Agent" }));

    expect(bounce).toHaveBeenCalledWith("session-inactive");
    // Bounce fixture doesn't flip activity — refetch and reattach must still
    // happen off the bounce succeeding, not off session.activity changing.
    await waitFor(() => expect(getSession.mock.calls.length).toBeGreaterThan(sessionCallsBefore));
    await waitFor(() =>
      expect(routerClient.sessionEventSources.length).toBeGreaterThan(streamCountBefore),
    );
  });

  it("Shell menu item toggles the shell dock via the cross-tree signal", async () => {
    const onToggle = vi.fn();
    window.addEventListener("mads:toggle-shell", onToggle);
    try {
      renderWorkspace();
      await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());

      fireEvent.click(screen.getByLabelText("Workspace actions"));
      fireEvent.click(screen.getByRole("button", { name: "Open shell" }));
      expect(onToggle).toHaveBeenCalled();
    } finally {
      window.removeEventListener("mads:toggle-shell", onToggle);
    }
  });

  it("shell menu item says Show shell (with a dot) for a live shell whose dock is hidden", async () => {
    const fake = makeFakeShellHost();
    fake.setShells([
      { sessionId: SESSION_ID, shellId: "s1", cwd: "/", pid: 1, startedAt: 0, attached: false },
    ]);
    window.mads = { shell: fake.host };
    renderWorkspace();
    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());

    fireEvent.click(screen.getByLabelText("Workspace actions"));

    await waitFor(() => expect(screen.getByRole("button", { name: /Show shell/ })).toBeTruthy());
  });

  it("kebab always shows all three actions; while active only Wake now is disabled, since deleting now closes the session itself", async () => {
    renderWorkspace({ sessionId: "session-active" });
    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());

    fireEvent.click(screen.getByLabelText("Workspace actions"));

    expect((screen.getByRole("button", { name: "Sleep now" }) as HTMLButtonElement).disabled).toBe(
      false,
    );
    const resume = screen.getByRole("button", { name: /Wake now/ }) as HTMLButtonElement;
    expect(resume.disabled).toBe(true);
    expect(resume.textContent).toContain("Already awake");
    // The warning moved into the confirm dialog: a dead Delete item would
    // pre-empt it the way the router's 409 used to.
    const del = screen.getByRole("button", { name: /Delete/ }) as HTMLButtonElement;
    expect(del.disabled).toBe(false);

    fireEvent.click(del);
    const dialog = screen.getByRole("alertdialog");
    expect(dialog.textContent).toContain("Deleting closes it first.");
    expect(within(dialog).getByRole("button", { name: "Close and delete" })).toBeTruthy();
  });

  it("kebab always shows all three actions; while inactive, Wake now, Delete, and Sleep now are all enabled", async () => {
    renderWorkspace({ sessionId: "session-inactive" });
    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());

    fireEvent.click(screen.getByLabelText("Workspace actions"));

    expect((screen.getByRole("button", { name: "Wake now" }) as HTMLButtonElement).disabled).toBe(
      false,
    );
    expect((screen.getByRole("button", { name: "Delete" }) as HTMLButtonElement).disabled).toBe(
      false,
    );
    // Close is never gated on the activity record — TerminateSession is safe
    // (and the only escape hatch) in every state, including one the record
    // claims is already inactive.
    expect((screen.getByRole("button", { name: "Sleep now" }) as HTMLButtonElement).disabled).toBe(
      false,
    );
  });

  // Sleep and wake are not this shell's to issue: it hands the session to the
  // surface's one controller (useSessionSleep), which is what makes "Sleep
  // now" here ask about unlanded commits exactly as the rail card's ⏸ does.
  // This shell used to call terminate itself, and skipped that question.
  it("Sleep now hands this session to the shared sleep controller, issuing nothing itself", async () => {
    const routerClient = new FakeRouterClient();
    const terminate = vi.spyOn(routerClient, "closeSession");
    const onSleep = vi.fn();
    renderWorkspace({ sessionId: "session-active", routerClient, onSleep });
    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());

    fireEvent.click(screen.getByLabelText("Workspace actions"));
    fireEvent.click(screen.getByText("Sleep now"));

    expect(onSleep).toHaveBeenCalledWith(expect.objectContaining({ id: "session-active" }));
    expect(terminate).not.toHaveBeenCalled();
  });

  it("Sleep now is offered, and still hands off, when the cached record already says inactive", async () => {
    const onSleep = vi.fn();
    renderWorkspace({ sessionId: "session-inactive", onSleep });
    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());

    fireEvent.click(screen.getByLabelText("Workspace actions"));
    const sleepItem = screen.getByRole("button", { name: "Sleep now" }) as HTMLButtonElement;
    expect(sleepItem.disabled).toBe(false);

    fireEvent.click(sleepItem);
    expect(onSleep).toHaveBeenCalledWith(expect.objectContaining({ id: "session-inactive" }));
  });

  it("Wake now hands this session to the same controller", async () => {
    const routerClient = new FakeRouterClient();
    const resume = vi.spyOn(routerClient, "resumeSession");
    const onWake = vi.fn();
    renderWorkspace({ sessionId: "session-inactive", routerClient, onWake });
    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());

    fireEvent.click(screen.getByLabelText("Workspace actions"));
    fireEvent.click(screen.getByText("Wake now"));

    expect(onWake).toHaveBeenCalledWith(expect.objectContaining({ id: "session-inactive" }));
    expect(resume).not.toHaveBeenCalled();
  });

  it("shows the session id as a footer row in the Workspace actions kebab", async () => {
    renderWorkspace({ sessionId: "session-active" });
    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());

    fireEvent.click(screen.getByLabelText("Workspace actions"));

    expect(screen.getByText("session-active")).toBeTruthy();
  });

  it("Delete opens a confirm dialog with cascade language and does not delete on cancel", async () => {
    const routerClient = new FakeRouterClient();
    const spy = vi.spyOn(routerClient, "deleteSession");
    renderWorkspace({ sessionId: "session-inactive", routerClient });
    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());

    fireEvent.click(screen.getByLabelText("Workspace actions"));
    fireEvent.click(screen.getByText("Delete"));

    const dialog = screen.getByRole("alertdialog");
    expect(dialog.textContent).toContain("event log survive");

    fireEvent.click(screen.getByText("Cancel"));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(spy).not.toHaveBeenCalled();
  });

  it("Delete confirms by calling routerClient.deleteSession and firing onClose", async () => {
    const routerClient = new FakeRouterClient();
    const spy = vi.spyOn(routerClient, "deleteSession");
    const onClose = vi.fn();
    renderWorkspace({ sessionId: "session-inactive", routerClient, onClose });
    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());

    fireEvent.click(screen.getByLabelText("Workspace actions"));
    fireEvent.click(screen.getByText("Delete"));
    fireEvent.click(within(screen.getByRole("alertdialog")).getByText("Delete"));

    await waitFor(() => expect(spy).toHaveBeenCalledWith("session-inactive"));
    await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
  });

  it("titles the Tasks badge with what it counts: open work, by status", () => {
    expect(openTasksTitle({ running: 1, ready: 3, blocked: 0 })).toBe("4 open tasks: 1 running, 3 ready");
    expect(openTasksTitle({ running: 0, ready: 0, blocked: 1 })).toBe("1 open task: 1 blocked");
  });
});
