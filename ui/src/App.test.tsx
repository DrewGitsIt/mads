import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { requestOpenSession } from "./appNavigation";
import { fixtureConfig, fixtureSessions } from "./router/fixtures";
import { FakeEventSource } from "./router/testFakeEventSource";

vi.mock("./library/LibrarySurface", () => ({
  LibrarySurface: () => {
    throw new Error("boom");
  },
}));

// Sessions/Launchpad mount several unrelated GET routes (sessions, tasks,
// workspaces, agents, providers) this test doesn't care about — anything
// not explicitly stubbed falls back to an empty list rather than throwing,
// since App.test.tsx's job is only the mount-time config gate/theme/flags
// wiring (HANDOFF §13), not exercising the Sessions surface itself.
function routedFetch(routes: Record<string, unknown>) {
  return vi.fn().mockImplementation((input: URL | string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    const path = new URL(input.toString()).pathname;
    const key = `${method} ${path}`;
    const body = key in routes ? routes[key] : [];
    return Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
  });
}

describe("App", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    delete document.documentElement.dataset.theme;
    delete document.documentElement.dataset.palette;
  });

  it("renders the shell while the mount-time config fetch is in flight, so the sessions list needn't wait for it", () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>(() => {})));
    const { container } = render(<App />);
    expect(container.firstChild).not.toBeNull();
  });

  it("renders the first-run wizard when onboarding_complete is false", async () => {
    vi.stubGlobal(
      "fetch",
      routedFetch({ "GET /config": { ...fixtureConfig, onboarding_complete: false } }),
    );
    render(<App />);
    await waitFor(() => expect(screen.getByText("Get started")).toBeTruthy());
  });

  it("applies persisted theme/palette and feature flags on mount, then renders the shell", async () => {
    vi.stubGlobal(
      "fetch",
      routedFetch({
        "GET /config": {
          ...fixtureConfig,
          theme: "light",
          palette: "space",
          feature_flags: { multi_repo: true, linked_sessions: false, remote_registry: false },
        },
      }),
    );
    render(<App />);
    await waitFor(() => expect(document.documentElement.dataset.theme).toBe("light"));
    expect(document.documentElement.dataset.palette).toBe("space");
    expect(screen.getByLabelText("Settings")).toBeTruthy();
  });

  // 2026-09-30: with the event stream refused, the rail kept a pre-wake
  // record ("Sleeping", a stale context %) and nothing said it was stale. The
  // warning is the shell's, above whichever surface is showing.
  it("warns that live updates are paused once the event stream keeps failing", async () => {
    // A reconnect after a drop re-reads the log's head first (a reset log).
    vi.stubGlobal("fetch", routedFetch({ "GET /config": fixtureConfig, "GET /events/head": { seq: 0 } }));
    render(<App />);
    await waitFor(() => expect(FakeEventSource.instances.length).toBeGreaterThan(0));
    expect(screen.queryByText("Live updates paused")).toBeNull();

    const liveSource = () => FakeEventSource.instances.filter((s) => !s.closed).at(-1);
    const first = liveSource()!;
    act(() => first.simulateError());
    expect(screen.queryByText("Live updates paused")).toBeNull(); // one drop is ordinary
    await waitFor(() => expect(liveSource() ?? first).not.toBe(first), { timeout: 3000 });
    act(() => liveSource()!.simulateError());

    await waitFor(() => expect(screen.getByText("Live updates paused")).toBeTruthy());
    expect(screen.getByRole("button", { name: "Reload" })).toBeTruthy();
  });

  it("says to restart mads when the services are older than this window (no GET /events/head)", async () => {
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const { resetEventsBusForTests } = await import("./router/events");
    resetEventsBusForTests(); // the production first connect: read the head
    const fetchMock = vi.fn((input: URL | string) => {
      const path = new URL(input.toString()).pathname;
      if (path === "/events/head") return Promise.resolve(new Response('{"error":"not found"}', { status: 404 }));
      if (path === "/config") return Promise.resolve(new Response(JSON.stringify(fixtureConfig), { status: 200 }));
      return Promise.resolve(new Response("[]", { status: 200 }));
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<App />);
    await waitFor(() => expect(screen.getByText("Restart mads")).toBeTruthy());
    expect(screen.getByText(/scripts\/app\.sh --restart/)).toBeTruthy();
    expect(screen.queryByText("Live updates paused")).toBeNull();
    const headReads = () =>
      fetchMock.mock.calls.filter(([input]) => new URL(input.toString()).pathname === "/events/head").length;
    expect(headReads()).toBe(1);
    await new Promise((r) => setTimeout(r, 1500));
    expect(headReads()).toBe(1);
    expect(FakeEventSource.instances).toHaveLength(0);
    error.mockRestore();
  });

  it("Settings rail item renders SettingsSurface", async () => {
    vi.stubGlobal("fetch", routedFetch({ "GET /config": fixtureConfig }));
    render(<App />);
    await waitFor(() => expect(screen.getByLabelText("Settings")).toBeTruthy());
    fireEvent.click(screen.getByLabelText("Settings"));
    await waitFor(() => expect(screen.getByText("Palette")).toBeTruthy());
  });

  it("leaving Settings with unsaved edits asks first: Keep editing stays, Discard goes", async () => {
    vi.stubGlobal("fetch", routedFetch({ "GET /config": fixtureConfig }));
    render(<App />);
    await waitFor(() => expect(screen.getByLabelText("Settings")).toBeTruthy());
    fireEvent.click(screen.getByLabelText("Settings"));
    await waitFor(() => expect(screen.getByLabelText("Concurrency limit")).toBeTruthy());
    fireEvent.change(screen.getByLabelText("Concurrency limit"), { target: { value: "7" } });

    fireEvent.click(screen.getByLabelText("Retro"));
    expect(screen.getByRole("alertdialog", { name: "Unsaved changes" })).toBeTruthy();
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Keep editing" }));
    expect(screen.getByLabelText("Concurrency limit")).toBeTruthy();

    fireEvent.click(screen.getByLabelText("Retro"));
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Discard" }));
    await waitFor(() => expect(screen.queryByLabelText("Concurrency limit")).toBeNull());
  });

  it("shows the failure screen when the config fetch rejects, and Retry refetches", async () => {
    let allowConfig = false;
    const fetchMock = vi.fn().mockImplementation((input: URL | string) => {
      const path = new URL(input.toString()).pathname;
      if (path === "/config") {
        if (!allowConfig) return Promise.reject(new TypeError("Failed to fetch"));
        return Promise.resolve(new Response(JSON.stringify(fixtureConfig), { status: 200 }));
      }
      return Promise.resolve(new Response(JSON.stringify([]), { status: 200 }));
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<App />);
    await waitFor(() => expect(screen.getByText(/Can.t reach/i)).toBeTruthy());

    allowConfig = true;
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() => expect(screen.getByLabelText("Settings")).toBeTruthy());
  });

  // docs/LOCAL-FRONT-DOOR-DESIGN.md H7: a window whose requests carry no
  // valid token gets 401 and must say so truthfully — mads couldn't use its
  // token file — not that the backend is down.
  it("shows the can't-reach-its-token screen on a 401, and Retry refetches", async () => {
    let signedIn = false;
    const fetchMock = vi.fn().mockImplementation((input: URL | string) => {
      const path = new URL(input.toString()).pathname;
      if (path === "/config" && !signedIn) {
        return Promise.resolve(
          new Response(JSON.stringify({ code: "unauthorized", error: "no token" }), { status: 401 }),
        );
      }
      const body = path === "/config" ? fixtureConfig : [];
      return Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<App />);
    await waitFor(() => expect(screen.getByText("mads can't reach its token")).toBeTruthy());
    expect(screen.getByText("$MADS_HOME/local.token")).toBeTruthy();
    expect(screen.getByText(/Restart the app/)).toBeTruthy();
    expect(screen.queryByText(/mads open/)).toBeNull();
    expect(screen.queryByText(/Can.t reach the mads backend/i)).toBeNull();

    signedIn = true;
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.getByLabelText("Settings")).toBeTruthy());
  });

  it("a surface crash renders the error boundary while the rail stays usable", async () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    stubRouterFetch();
    render(<App />);
    await waitFor(() => expect(screen.getByLabelText("Settings")).toBeTruthy());

    fireEvent.click(screen.getByLabelText("Library"));
    await waitFor(() => expect(screen.getByText("Something went wrong")).toBeTruthy());
    expect(screen.getByText("boom")).toBeTruthy();

    expect(screen.getByLabelText("Settings")).toBeTruthy();
    fireEvent.click(screen.getByLabelText("Settings"));
    await waitFor(() => expect(screen.getByText("Palette")).toBeTruthy());

    consoleError.mockRestore();
  });
});

const sessions = [fixtureSessions["session-active"], fixtureSessions["session-inactive"]];

function stubRouterFetch() {
  const fetchMock = vi.fn().mockImplementation((input: URL | string) => {
    const url = input.toString();
    if (url.includes("/sessions") && !url.includes("/pipelines")) {
      return Promise.resolve(new Response(JSON.stringify(sessions), { status: 200 }));
    }
    if (url.includes("/tasks")) {
      return Promise.resolve(new Response("[]", { status: 200 }));
    }
    if (url.includes("/pipelines")) {
      return Promise.resolve(new Response(JSON.stringify({ run_id: "run-1" }), { status: 200 }));
    }
    if (url.includes("/config")) {
      return Promise.resolve(new Response(JSON.stringify(fixtureConfig), { status: 200 }));
    }
    return Promise.resolve(new Response("[]", { status: 200 }));
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("App command palette (W6.11)", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("toggles the command palette open and closed on Cmd+Shift+P", async () => {
    stubRouterFetch();
    render(<App />);
    await waitFor(() => expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy());

    expect(screen.queryByPlaceholderText(/jump to a session/i)).toBeNull();

    fireEvent.keyDown(document, { key: "P", metaKey: true, shiftKey: true });
    expect(screen.getByPlaceholderText(/jump to a session/i)).toBeTruthy();

    fireEvent.keyDown(document, { key: "P", metaKey: true, shiftKey: true });
    expect(screen.queryByPlaceholderText(/jump to a session/i)).toBeNull();
  });

  it("dispatching a jump-session command selects that session in the rail", async () => {
    stubRouterFetch();
    render(<App />);
    await waitFor(() => expect(screen.getByText("Investigate flaky metrics query test")).toBeTruthy());

    fireEvent.keyDown(document, { key: "P", metaKey: true, shiftKey: true });
    fireEvent.click(screen.getByTestId("jump:session-inactive"));

    await waitFor(() => {
      const row = screen.getByText("Investigate flaky metrics query test").closest('[role="button"]');
      expect(row?.className.includes("selected")).toBe(true);
    });

    // Plain jumps always land on Conversation, not wherever a prior jump left off.
    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "Conversation" }).getAttribute("aria-selected")).toBe(
        "true",
      ),
    );
  });

  it("Run pipeline… opens that session's Tasks tab in Plan, starting nothing", async () => {
    const fetchMock = stubRouterFetch();
    render(<App />);
    await waitFor(() => expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy());

    fireEvent.keyDown(document, { key: "P", metaKey: true, shiftKey: true });
    fireEvent.click(screen.getByTestId("run-pipeline:session-active"));

    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "Tasks" }).getAttribute("aria-selected")).toBe("true"),
    );
    expect(await screen.findByRole("button", { name: /Run \d+ tasks?/ })).toBeTruthy();
    expect(
      fetchMock.mock.calls.some(
        (c) => (c[0] as URL | string).toString().endsWith("/pipelines") && c[1]?.method === "POST",
      ),
    ).toBe(false);
  });

  it("the open-session signal with a run lands on that session's Tasks tab", async () => {
    stubRouterFetch();
    render(<App />);
    await waitFor(() => expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy());

    act(() => requestOpenSession("session-active", { run: "new" }));

    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "Tasks" }).getAttribute("aria-selected")).toBe("true"),
    );
  });

  it("Ctrl+` toggles the Shell dock while a session workspace is open", async () => {
    stubRouterFetch();
    render(<App />);
    await waitFor(() => expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy());

    fireEvent.keyDown(document, { key: "P", metaKey: true, shiftKey: true });
    fireEvent.click(screen.getByTestId("jump:session-active"));
    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "Conversation" }).getAttribute("aria-selected")).toBe(
        "true",
      ),
    );

    expect(screen.queryByText(/Shell requires the Electron shell/)).toBeNull();

    fireEvent.keyDown(document, { key: "`", ctrlKey: true });
    expect(screen.getByText(/Shell requires the Electron shell/)).toBeTruthy();

    fireEvent.keyDown(document, { key: "`", ctrlKey: true });
    expect(screen.queryByText(/Shell requires the Electron shell/)).toBeNull();
  });

  it("Ctrl+` toggles the Shell dock when the key arrives Unidentified (Electron on Wayland)", async () => {
    stubRouterFetch();
    render(<App />);
    await waitFor(() => expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy());

    fireEvent.keyDown(document, { key: "P", metaKey: true, shiftKey: true });
    fireEvent.click(screen.getByTestId("jump:session-active"));
    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "Conversation" }).getAttribute("aria-selected")).toBe(
        "true",
      ),
    );

    fireEvent.keyDown(document, { key: "Unidentified", code: "Backquote", ctrlKey: true });
    expect(screen.getByText(/Shell requires the Electron shell/)).toBeTruthy();
  });

  it("Ctrl+` has no effect when no session workspace is open", async () => {
    stubRouterFetch();
    render(<App />);
    await waitFor(() => expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy());

    fireEvent.keyDown(document, { key: "`", ctrlKey: true });
    expect(screen.queryByText(/Shell requires the Electron shell/)).toBeNull();
  });
});
