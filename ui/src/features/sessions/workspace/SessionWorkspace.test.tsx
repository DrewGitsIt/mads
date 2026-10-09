import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { SessionWorkspace, openTasksTitle } from "./SessionWorkspace";
import { FakeRouterClient } from "../../../router/fixtures";
import { FakePipelineClient } from "../../../pipeline/client/fixtures";
import { FakeEventSource } from "../../../router/testFakeEventSource";
import { makeFakeShellHost } from "../../../conversation/ShellDock/fakeShellHost";
import { resetShellDockCache } from "../../../conversation/ShellDock/dockState";

const SESSION_ID = "session-active";

function renderWorkspace(props: Partial<Parameters<typeof SessionWorkspace>[0]> = {}) {
  return render(
    <SessionWorkspace
      sessionId={SESSION_ID}
      routerClient={new FakeRouterClient()}
      pipelineClient={new FakePipelineClient()}
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

  it("defaults to the Conversation tab, lazy-mounting the other three", async () => {
    renderWorkspace();

    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "Conversation" }).getAttribute("aria-selected")).toBe(
        "true",
      ),
    );
    expect(screen.getByRole("tab", { name: "Tasks" }).getAttribute("aria-selected")).toBe("false");
    expect(screen.getByRole("tab", { name: "Files" }).getAttribute("aria-selected")).toBe("false");
    expect(screen.getByRole("tab", { name: "Pipeline" }).getAttribute("aria-selected")).toBe(
      "false",
    );

    expect(screen.queryByRole("tabpanel", { name: "Tasks" })).toBeNull();
    expect(screen.queryByRole("tabpanel", { name: "Files" })).toBeNull();
    expect(screen.queryByRole("tabpanel", { name: "Pipeline" })).toBeNull();
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
    await waitFor(() => expect(screen.getByText("Graph")).toBeTruthy());

    fireEvent.click(screen.getByText("Graph"));
    await waitFor(() => expect(screen.getByText("Graph").className.includes("primary")).toBe(true));

    fireEvent.click(screen.getByRole("tab", { name: "Conversation" }));
    fireEvent.click(screen.getByRole("tab", { name: "Tasks" }));

    expect(screen.getByText("Graph").className.includes("primary")).toBe(true);
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
          onClose={vi.fn()}
      />,
    );
    expect(screen.getByRole("tab", { name: "Tasks" }).textContent).toBe("Tasks");
  });

  it("shows a pipeline run-dot while a run is in flight, clearing on completion", async () => {
    renderWorkspace();
    await waitFor(() => expect(screen.getByRole("tab", { name: "Pipeline" })).toBeTruthy());

    const source = FakeEventSource.instances.find((s) => s.url.includes("pipeline.started"));
    expect(screen.getByRole("tab", { name: "Pipeline" }).querySelector('[title="run in progress"]')).toBeNull();

    act(() => {
      source?.emit(
        "pipeline.started",
        JSON.stringify({ seq: 1, type: "pipeline.started", actor: "user", session_id: SESSION_ID }),
      );
    });
    await waitFor(() =>
      expect(
        screen.getByRole("tab", { name: "Pipeline" }).querySelector('[title="run in progress"]'),
      ).toBeTruthy(),
    );

    act(() => {
      source?.emit(
        "pipeline.completed",
        JSON.stringify({ seq: 2, type: "pipeline.completed", actor: "user", session_id: SESSION_ID }),
      );
    });
    await waitFor(() =>
      expect(
        screen.getByRole("tab", { name: "Pipeline" }).querySelector('[title="run in progress"]'),
      ).toBeNull(),
    );
  });

  it("folds Shell and Restart Agent into the single workspace kebab; Restart bounces the agent", async () => {
    const routerClient = new FakeRouterClient();
    const bounce = vi.spyOn(routerClient, "bounce");
    render(
      <SessionWorkspace
        sessionId={SESSION_ID}
        routerClient={routerClient}
        pipelineClient={new FakePipelineClient()}
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

  it("Close calls routerClient.closeSession for this session", async () => {
    const routerClient = new FakeRouterClient();
    const spy = vi.spyOn(routerClient, "closeSession");
    renderWorkspace({ sessionId: "session-active", routerClient });
    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());

    fireEvent.click(screen.getByLabelText("Workspace actions"));
    fireEvent.click(screen.getByText("Sleep now"));

    expect(spy).toHaveBeenCalledWith("session-active");
  });

  it("Close is offered and issues terminate even when the cached record already says inactive", async () => {
    const routerClient = new FakeRouterClient();
    const spy = vi.spyOn(routerClient, "closeSession");
    renderWorkspace({ sessionId: "session-inactive", routerClient });
    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());

    fireEvent.click(screen.getByLabelText("Workspace actions"));
    const close = screen.getByRole("button", { name: "Sleep now" }) as HTMLButtonElement;
    expect(close.disabled).toBe(false);

    fireEvent.click(close);
    expect(spy).toHaveBeenCalledWith("session-inactive");
  });

  it("Close/terminate failure surfaces in the action-error toast", async () => {
    const routerClient = new FakeRouterClient();
    vi.spyOn(routerClient, "closeSession").mockRejectedValue(
      new Error("terminate session: child unresponsive"),
    );
    renderWorkspace({ sessionId: "session-active", routerClient });
    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());

    fireEvent.click(screen.getByLabelText("Workspace actions"));
    fireEvent.click(screen.getByText("Sleep now"));

    await waitFor(() => expect(screen.getByText("Session action failed")).toBeTruthy());
    expect(screen.getByText("terminate session: child unresponsive")).toBeTruthy();
  });

  it("Wake now calls routerClient.resumeSession for this session", async () => {
    const routerClient = new FakeRouterClient();
    const spy = vi.spyOn(routerClient, "resumeSession");
    renderWorkspace({ sessionId: "session-inactive", routerClient });
    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());

    fireEvent.click(screen.getByLabelText("Workspace actions"));
    fireEvent.click(screen.getByText("Wake now"));

    expect(spy).toHaveBeenCalledWith("session-inactive");
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
