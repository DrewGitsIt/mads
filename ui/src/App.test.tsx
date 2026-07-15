import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { fixtureConfig } from "./router/fixtures";
import { FakeEventSource } from "./router/testFakeEventSource";

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
});
