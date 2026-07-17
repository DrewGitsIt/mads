import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
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

  it("renders nothing while the mount-time config fetch is in flight", () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise(() => {})));
    const { container } = render(<App />);
    expect(container.firstChild).toBeNull();
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

  it("Settings rail item renders SettingsSurface", async () => {
    vi.stubGlobal("fetch", routedFetch({ "GET /config": fixtureConfig }));
    render(<App />);
    await waitFor(() => expect(screen.getByLabelText("Settings")).toBeTruthy());
    fireEvent.click(screen.getByLabelText("Settings"));
    await waitFor(() => expect(screen.getByText("Palette")).toBeTruthy());
  });

  it("shows the failure screen when the config fetch rejects, and Retry refetches", async () => {
    let calls = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: URL | string, init?: RequestInit) => {
        if (calls++ === 0) return Promise.reject(new TypeError("Failed to fetch"));
        return routedFetch({ "GET /config": fixtureConfig })(input, init);
      }),
    );
    render(<App />);
    await waitFor(() => expect(screen.getByText(/can't reach/i)).toBeTruthy());

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

  it("toggles the command palette open and closed on Cmd+K", async () => {
    stubRouterFetch();
    render(<App />);
    await waitFor(() => expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy());

    expect(screen.queryByPlaceholderText(/jump to a session/i)).toBeNull();

    fireEvent.keyDown(document, { key: "k", metaKey: true });
    expect(screen.getByPlaceholderText(/jump to a session/i)).toBeTruthy();

    fireEvent.keyDown(document, { key: "k", metaKey: true });
    expect(screen.queryByPlaceholderText(/jump to a session/i)).toBeNull();
  });

  it("dispatching a jump-session command selects that session in the rail", async () => {
    stubRouterFetch();
    render(<App />);
    await waitFor(() => expect(screen.getByText("Investigate flaky metrics query test")).toBeTruthy());

    fireEvent.keyDown(document, { key: "k", metaKey: true });
    fireEvent.click(screen.getByTestId("jump:session-inactive"));

    await waitFor(() => {
      const row = screen.getByText("Investigate flaky metrics query test").closest("button");
      expect(row?.className.includes("selected")).toBe(true);
    });

    // Plain jumps always land on Conversation, not wherever a prior jump left off.
    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "Conversation" }).getAttribute("aria-selected")).toBe(
        "true",
      ),
    );
  });

  it("dispatching a start-pipeline command posts to the pipelines endpoint", async () => {
    const fetchMock = stubRouterFetch();
    render(<App />);
    await waitFor(() => expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy());

    fireEvent.keyDown(document, { key: "k", metaKey: true });
    fireEvent.click(screen.getByTestId("start-pipeline:session-active"));

    await waitFor(() => {
      const call = fetchMock.mock.calls.find((c) => (c[0] as URL | string).toString().includes("/pipelines"));
      expect(call).toBeTruthy();
      expect(call?.[1]?.body).toContain("session-active");
      expect(call?.[1]?.body).toContain("default-implementation");
    });

    // "start a pipeline" lands on that session's Pipeline tab, not Conversation.
    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "Pipeline" }).getAttribute("aria-selected")).toBe(
        "true",
      ),
    );
  });

  it("Ctrl+` toggles the Shell dock while a session workspace is open", async () => {
    stubRouterFetch();
    render(<App />);
    await waitFor(() => expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy());

    fireEvent.keyDown(document, { key: "k", metaKey: true });
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

  it("Ctrl+` has no effect when no session workspace is open", async () => {
    stubRouterFetch();
    render(<App />);
    await waitFor(() => expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy());

    fireEvent.keyDown(document, { key: "`", ctrlKey: true });
    expect(screen.queryByText(/Shell requires the Electron shell/)).toBeNull();
  });
});
