// @vitest-environment jsdom
// Same jsdom rationale as ConversationTab.test.tsx: ChatRenderer renders
// markdown through DOMPurify, which doesn't sanitize under happy-dom.
//
// A message from outside MADS arriving on the live stream: it renders at
// once, opens a turn when the agent was idle, and the turn-end transcript
// refetch replaces it without a duplicate.
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, act } from "@testing-library/react";
import { ConversationTab } from "./ConversationTab";
import { FakeRouterClient } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
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

  it("shows an idle-arriving inbound live, then reconciles with the transcript without a duplicate", async () => {
    const client = new FakeRouterClient();
    const getTranscript = vi.spyOn(client, "getTranscript").mockResolvedValue([]);
    render(<ConversationTab sessionId="session-active" routerClient={client} />);
    await waitFor(() => expect(client.sessionEventSources.length).toBeGreaterThan(0));
    await waitFor(() => expect(screen.getByPlaceholderText(/message the agent/i)).toBeTruthy());
    const source = client.sessionEventSources.at(-1)!;

    act(() => {
      source.emit({ kind: "inbound", text: "please rebase", inbound });
    });
    // It opened a turn nobody typed: the agent is working, and the message is on screen.
    expect(screen.getByRole("button", { name: "Stop generating" })).toBeTruthy();
    expect(screen.getAllByRole("note")).toHaveLength(1);
    expect(screen.getByRole("note").textContent).toContain("from Leader refactor");

    getTranscript.mockResolvedValue([
      { role: "inbound", text: "please rebase", inbound, timestamp: "2026-10-19T10:00:00Z" },
      { role: "agent", text: "Rebased." },
    ]);
    await act(async () => {
      source.emit({ kind: "text", text: "Rebased." });
      source.emit({ kind: "turn_end" });
      await Promise.resolve();
    });
    await waitFor(() => expect(getTranscript).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Stop generating" })).toBeNull());
    expect(screen.getAllByRole("note")).toHaveLength(1);
    expect(screen.getAllByText(/please rebase/)).toHaveLength(1);
  });
});
