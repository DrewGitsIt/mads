import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { SessionWorkspace } from "./SessionWorkspace";
import { FakeRouterClient } from "../../../router/fixtures";
import { FakePipelineClient } from "../../../pipeline/client/fixtures";
import { FakeEventSource } from "../../../router/testFakeEventSource";
import { resetShellDockCache } from "../../../conversation/ShellDock/dockState";

// The tabs stubbed out: several read the session record themselves
// (ConversationTab, the Tasks tab's workspace lookup), and these tests count
// the shell's GET /sessions/:id alone.
vi.mock("./tabRegistry", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./tabRegistry")>();
  const Stub = () => null;
  return {
    ...actual,
    WORKSPACE_TABS: actual.WORKSPACE_TABS.map((t) => ({ ...t, component: Stub })),
  };
});

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

// The bus replays its durable log on connect, so the shell's
// refetch-on-session-event saw one GET /sessions/:id per replayed event.
describe("SessionWorkspace session refetch", () => {
  let seq = 1_000_000;
  const emitSessionUpdated = (sessionId: string) => {
    const source = FakeEventSource.instances.find((s) => s.url.includes("session.updated"));
    seq += 1;
    source?.emit(
      "session.updated",
      JSON.stringify({ seq, timestamp: "2026-11-09T10:00:00Z", type: "session.updated", session_id: sessionId, actor: "router" }),
    );
  };
  const quiet = (ms: number) => act(() => new Promise<void>((r) => setTimeout(r, ms)));
  const flushMicrotasks = () =>
    act(async () => {
      for (let i = 0; i < 10; i++) await Promise.resolve();
    });
  function deferred<T>() {
    let resolve!: (v: T) => void;
    const promise = new Promise<T>((res) => (resolve = res));
    return { promise, resolve };
  }

  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    window.localStorage.clear();
    resetShellDockCache();
  });

  it("collapses a burst of 200 session.updated for the open session into at most 2 GET /sessions/:id", async () => {
    const routerClient = new FakeRouterClient();
    const getSession = vi.spyOn(routerClient, "getSession");
    renderWorkspace({ routerClient });
    await waitFor(() => expect(screen.getByTitle("Rename session").textContent).toBe("Add retry backoff to the ingest worker"));
    const before = getSession.mock.calls.length;

    act(() => {
      for (let i = 0; i < 200; i++) emitSessionUpdated(SESSION_ID);
    });
    await quiet(700);
    const after = getSession.mock.calls.length - before;
    expect(after).toBeGreaterThanOrEqual(1);
    expect(after).toBeLessThanOrEqual(2);
  });

  it("an event-driven refetch still lands the new record", async () => {
    const routerClient = new FakeRouterClient();
    renderWorkspace({ routerClient });
    await waitFor(() => expect(screen.getByTitle("Rename session").textContent).toBe("Add retry backoff to the ingest worker"));

    // A fresh object, as the wire gives (the fixture hands back one mutable record).
    const base = await routerClient.getSession(SESSION_ID);
    vi.spyOn(routerClient, "getSession").mockResolvedValue({ ...base, name: "Renamed elsewhere" });
    act(() => emitSessionUpdated(SESSION_ID));
    await waitFor(() => expect(screen.getByTitle("Rename session").textContent).toBe("Renamed elsewhere"));
  });

  it("an older, slower response can't be the last word: the fetch after it wins", async () => {
    const routerClient = new FakeRouterClient();
    const base = await routerClient.getSession(SESSION_ID);
    const slow = deferred<typeof base>();
    const getSession = vi
      .spyOn(routerClient, "getSession")
      .mockReturnValueOnce(slow.promise)
      .mockResolvedValue({ ...base, name: "Fresh" });
    renderWorkspace({ routerClient });

    // The mount fetch is in flight; an update arrives meanwhile.
    act(() => emitSessionUpdated(SESSION_ID));
    await quiet(600);
    expect(getSession.mock.calls.filter(([id]) => id === SESSION_ID)).toHaveLength(1); // single-flight

    // The slow, older answer lands — then the follow-up's fresher one.
    await act(async () => slow.resolve({ ...base, name: "Stale" }));
    await waitFor(() => expect(screen.getByTitle("Rename session").textContent).toBe("Fresh"));
    await quiet(100);
    expect(screen.getByTitle("Rename session").textContent).toBe("Fresh");
    expect(getSession.mock.calls.filter(([id]) => id === SESSION_ID)).toHaveLength(2);
  });

  it("drops a response for a session this shell no longer shows", async () => {
    const routerClient = new FakeRouterClient();
    const active = await routerClient.getSession(SESSION_ID);
    const inactive = await routerClient.getSession("session-inactive");
    const slow = deferred<typeof active>();
    vi.spyOn(routerClient, "getSession").mockImplementation((id) =>
      id === SESSION_ID ? slow.promise : Promise.resolve(inactive),
    );
    const { rerender } = renderWorkspace({ routerClient });
    rerender(
      <SessionWorkspace
        sessionId="session-inactive"
        routerClient={routerClient}
        pipelineClient={new FakePipelineClient()}
        onSleep={vi.fn()}
        onWake={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    await waitFor(() => expect(screen.getByTitle("Rename session").textContent).toBe(inactive.name));
    await act(async () => slow.resolve({ ...active, name: "Late, for the old session" }));
    await quiet(50);
    expect(screen.getByTitle("Rename session").textContent).toBe(inactive.name);
  });

  it("Restart Agent's refetch is immediate, not debounced", async () => {
    const routerClient = new FakeRouterClient();
    vi.spyOn(routerClient, "bounce").mockResolvedValue(undefined);
    const getSession = vi.spyOn(routerClient, "getSession");
    renderWorkspace({ routerClient, sessionId: "session-inactive" });
    await waitFor(() => expect(screen.getByLabelText("Workspace actions")).toBeTruthy());
    await quiet(20);
    const before = getSession.mock.calls.length;

    fireEvent.click(screen.getByLabelText("Workspace actions"));
    fireEvent.click(screen.getByRole("button", { name: "Restart Agent" }));
    // No timer advanced — only the bounce's promise settling.
    await flushMicrotasks();
    expect(getSession.mock.calls.length).toBe(before + 1);
  });

  it("a failed rename reverts to server truth immediately, not debounced", async () => {
    const routerClient = new FakeRouterClient();
    vi.spyOn(routerClient, "renameSession").mockRejectedValue(new Error("nope"));
    const getSession = vi.spyOn(routerClient, "getSession");
    renderWorkspace({ routerClient });
    const nameButton = await screen.findByTitle("Rename session");
    await waitFor(() => expect(nameButton.textContent).toBe("Add retry backoff to the ingest worker"));
    const before = getSession.mock.calls.length;

    fireEvent.click(nameButton);
    const input = screen.getByLabelText("Session name") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "Will fail" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(screen.getByTitle("Rename session").textContent).toBe("Will fail"); // optimistic

    await flushMicrotasks();
    expect(getSession.mock.calls.length).toBe(before + 1);
    await waitFor(() =>
      expect(screen.getByTitle("Rename session").textContent).toBe("Add retry backoff to the ingest worker"),
    );
  });
});
