import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App } from "./App";
import { FakeEventSource } from "./router/testFakeEventSource";
import { fixtureSessions } from "./router/fixtures";

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
    return Promise.resolve(new Response("[]", { status: 200 }));
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("App", () => {
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
  });
});
