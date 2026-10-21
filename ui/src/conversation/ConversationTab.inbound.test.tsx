// @vitest-environment jsdom
// Same jsdom rationale as ConversationTab.test.tsx: ChatRenderer renders
// markdown through DOMPurify, which doesn't sanitize under happy-dom.
//
// A message from outside MADS arriving on the live stream: a keyed inbound
// frame renders at once and opens a turn when the agent was idle, and the
// turn-end page's stored row (same key) takes its place without a duplicate.
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, act } from "@testing-library/react";
import { ConversationTab } from "./ConversationTab";
import { FakeRouterClient } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import { frames, page } from "./keyed/testFrames";
import type { InboundSource } from "../router/types";

vi.mock("@monaco-editor/react", () => ({
  Editor: () => <textarea data-testid="fake-monaco-editor" />,
}));

if (!window.matchMedia) {
  window.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }) as unknown as MediaQueryList;
}

if (typeof window.ResizeObserver === "undefined") {
  window.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
}

const inbound: InboundSource = { channel: "peer", provider: "claude-code", session_name: "Leader refactor" };

describe("ConversationTab — inbound messages", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows an idle-arriving inbound live, then merges its stored row by key without a duplicate", async () => {
    const client = new FakeRouterClient();
    const getPage = vi.spyOn(client, "getTranscriptPage").mockResolvedValue(page([]));
    render(<ConversationTab sessionId="session-active" routerClient={client} />);
    await waitFor(() => expect(client.v2Sources.length).toBeGreaterThan(0));
    await waitFor(() => expect(screen.getByPlaceholderText(/message the agent/i)).toBeTruthy());
    const source = frames(client);

    act(() => {
      source.frame("inbound", { key: "in:1", text: "please rebase", inbound, timestamp: "2026-10-19T10:00:00Z" });
    });
    // It opened a turn nobody typed: the agent is working, and the message is on screen.
    expect(screen.getByRole("button", { name: "Stop generating" })).toBeTruthy();
    expect(screen.getAllByRole("note")).toHaveLength(1);
    expect(screen.getByRole("note").textContent).toContain("from Leader refactor");
    const note = screen.getByRole("note");

    let reply = "";
    act(() => {
      reply = source.text("Rebased.");
    });
    getPage.mockResolvedValue(
      page([
        { role: "inbound", key: "in:1", text: "please rebase", inbound, timestamp: "2026-10-19T10:00:00Z" },
        { role: "agent", key: reply, text: "Rebased.", timestamp: "2026-10-19T10:00:01Z" },
      ]),
    );
    act(() => source.emit({ kind: "turn_end" }));
    await waitFor(() => expect(getPage).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Stop generating" })).toBeNull());
    expect(screen.getAllByRole("note")).toHaveLength(1);
    expect(screen.getAllByText(/please rebase/)).toHaveLength(1);
    expect(screen.getAllByText("Rebased.")).toHaveLength(1);
    // The stored row took the live one's place: same element, same React key.
    expect(screen.getByRole("note")).toBe(note);
  });

  it("an inbound without a key opens a turn but waits for the stored row to show", async () => {
    const client = new FakeRouterClient();
    const getPage = vi.spyOn(client, "getTranscriptPage").mockResolvedValue(page([]));
    render(<ConversationTab sessionId="session-active" routerClient={client} />);
    await waitFor(() => expect(client.v2Sources.length).toBeGreaterThan(0));
    await waitFor(() => expect(screen.getByPlaceholderText(/message the agent/i)).toBeTruthy());
    const source = frames(client);

    act(() => source.frame("inbound", { text: "please rebase", inbound }));
    expect(screen.getByRole("button", { name: "Stop generating" })).toBeTruthy();
    expect(screen.queryByRole("note")).toBeNull();

    getPage.mockResolvedValue(
      page([{ role: "inbound", key: "in:1", text: "please rebase", inbound, timestamp: "2026-10-19T10:00:00Z" }]),
    );
    act(() => source.emit({ kind: "turn_end" }));
    await waitFor(() => expect(screen.getAllByRole("note")).toHaveLength(1));
    expect(screen.getByRole("note").textContent).toContain("from Leader refactor");
  });
});
