// @vitest-environment jsdom
// Same jsdom rationale as ConversationTab.test.tsx: ChatRenderer renders
// markdown through DOMPurify, which doesn't sanitize under happy-dom.
//
// A paced (words-per-minute) reveal across the turn's end: the agent's
// burst plays out at the chosen pace, the composer frees up as soon as the
// agent is done, the turn-end page merges by key without cutting the reveal
// short, Show all skips ahead, and a new turn (a send, or one nobody here
// sent) shows the rest of the reply at once, ahead of what comes next.
//
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react";
import { ConversationTab } from "./ConversationTab";
import { FakeRouterClient, fixtureConfig } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import { frames as framesOf, page, type FrameDriver } from "./keyed/testFrames";

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

// Hand-driven animation frames: each tick() advances the clock and runs the
// frames queued so far, so a test reveals exactly as many words as it means to.
let rafQueue: FrameRequestCallback[] = [];
let now = 0;
function tick(ms: number) {
  act(() => {
    now += ms;
    const run = rafQueue;
    rafQueue = [];
    for (const cb of run) cb(now);
  });
}

const REPLY = "one two three four five six seven eight nine ten eleven twelve.";

async function mountPaced(client: FakeRouterClient): Promise<FrameDriver> {
  vi.spyOn(client, "getConfig").mockResolvedValue({ ...fixtureConfig, stream_speed: "custom", stream_wpm: 600 });
  render(<ConversationTab sessionId="session-active" routerClient={client} />);
  await waitFor(() => expect(client.v2Sources.length).toBeGreaterThan(0));
  await waitFor(() => expect(screen.getByPlaceholderText(/message the agent/i)).toBeTruthy());
  // Let the pace load before anything streams.
  await act(async () => {
    await Promise.resolve();
  });
  const source = framesOf(client);
  // A fresh connection: nothing to replay, so what follows is live and paced.
  act(() => {
    source.hello();
    source.replayEnd();
  });
  return source;
}

function liveText(): string {
  return screen.getByTestId("conversation-stream").textContent ?? "";
}

describe("ConversationTab — paced reveal", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    rafQueue = [];
    now = 0;
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      rafQueue.push(cb);
      return rafQueue.length;
    });
    vi.stubGlobal("cancelAnimationFrame", () => {
      rafQueue = [];
    });
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("plays a burst out at the chosen pace, past the turn's end, with the composer free", async () => {
    const client = new FakeRouterClient();
    const getPage = vi.spyOn(client, "getTranscriptPage").mockResolvedValue(page([]));
    const source = await mountPaced(client);

    let key = "";
    act(() => void source.user("go"));
    act(() => {
      key = source.text(REPLY);
    });
    // The turn-end page holds the reply's stored row under the same key.
    getPage.mockResolvedValue(page([{ role: "agent", key, text: REPLY, timestamp: "2026-10-21T10:00:00Z" }]));
    act(() => source.emit({ kind: "turn_end" }));
    tick(0); // the loop's first frame sets its clock
    tick(100); // 600 wpm = 10 words/s → one word
    expect(liveText()).toContain("one");
    expect(liveText()).not.toContain("three");

    // The agent is done: no Stop, the send button sends.
    expect(screen.queryByRole("button", { name: "Stop generating" })).toBeNull();
    expect(screen.getByRole("button", { name: "Send" })).toBeTruthy();
    // The turn end's one page fetch merges by key: the stored row takes the
    // live one's place without cutting the reveal short.
    await waitFor(() => expect(getPage).toHaveBeenCalledTimes(2));
    await act(async () => {
      await Promise.resolve();
    });
    expect(liveText()).not.toContain("three");

    // 1.1s of reading left — under the two-second mark, so no Show all.
    expect(screen.queryByRole("button", { name: "Show all" })).toBeNull();
    for (let i = 0; i < 12; i++) tick(100);
    expect(liveText()).toContain("twelve.");
    expect(screen.getAllByText(/twelve\./)).toHaveLength(1);
    // Drained: no further fetch, no replacement.
    expect(getPage).toHaveBeenCalledTimes(2);
  });

  it("Show all reveals the rest at once", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscriptPage").mockResolvedValue(page([]));
    const source = await mountPaced(client);

    const long = Array.from({ length: 60 }, (_, i) => `w${i}`).join(" ") + ".";
    act(() => {
      source.emit({ kind: "text", text: long });
      source.emit({ kind: "turn_end" });
    });
    tick(0);
    tick(100);
    expect(liveText()).not.toContain("w59.");
    fireEvent.click(screen.getByRole("button", { name: "Show all" }));
    expect(liveText()).toContain("w59.");
    await waitFor(() => expect(screen.queryByRole("button", { name: "Show all" })).toBeNull());
  });

  it("a send mid-reveal puts the whole reply in the history ahead of the new message", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscriptPage").mockResolvedValue(page([]));
    const source = await mountPaced(client);

    act(() => {
      source.emit({ kind: "text", text: REPLY });
      source.emit({ kind: "turn_end" });
    });
    tick(0);
    tick(100);
    expect(liveText()).not.toContain("twelve.");

    const composer = screen.getByPlaceholderText(/message the agent/i);
    fireEvent.change(composer, { target: { value: "next question" } });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Send" }));
      await Promise.resolve();
    });
    // The send opened a turn: the rest of the reply shows at once.
    expect(liveText()).toContain("twelve.");
    act(() => void source.user("next question"));

    const all = liveText();
    expect(all).toContain("twelve.");
    expect(all.indexOf("twelve.")).toBeLessThan(all.indexOf("next question"));
    expect(screen.getAllByText(/twelve\./)).toHaveLength(1);
  });

  it("a late 'working' from the control plane doesn't cut a reveal short", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscriptPage").mockResolvedValue(page([]));
    const source = await mountPaced(client);

    act(() => {
      source.emit({ kind: "text", text: REPLY });
      source.emit({ kind: "turn_end" });
    });
    tick(0);
    tick(100);
    // The turn's own "working" can land after the stream already ended it.
    const control = FakeEventSource.instances.find((s) => s.url.includes("session.agent_state"));
    act(() => {
      control!.emit(
        "session.agent_state",
        JSON.stringify({
          seq: 90,
          type: "session.agent_state",
          actor: "system",
          session_id: "session-active",
          payload: { state: "working", since: new Date().toISOString() },
        }),
      );
    });
    expect(liveText()).not.toContain("twelve.");
    expect(screen.queryByRole("button", { name: "Stop generating" })).toBeNull();
  });

  it("a new turn nobody here sent finishes a still-revealing reply at once, ahead of the new turn", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscriptPage").mockResolvedValue(page([]));
    const source = await mountPaced(client);

    act(() => {
      source.emit({ kind: "text", text: REPLY });
      source.emit({ kind: "turn_end" });
    });
    tick(0);
    tick(100);
    expect(liveText()).not.toContain("twelve.");
    // A turn this pane didn't send (a linked session's inject) starts: the
    // last reply is shown whole at once, the new message after it.
    act(() => void source.user("from the leader"));
    const all = liveText();
    expect(all).toContain("twelve.");
    expect(all.indexOf("twelve.")).toBeLessThan(all.indexOf("from the leader"));
    expect(screen.getAllByText(/twelve\./)).toHaveLength(1);

    // The new turn's reply paces in after it.
    act(() => void source.text("Fresh reply begins. "));
    tick(0); // the new reveal's loop sets its clock
    tick(100);
    const after = liveText();
    expect(after).toContain("Fresh");
    expect(after.indexOf("twelve.")).toBeLessThan(after.indexOf("Fresh"));
    expect(after.indexOf("from the leader")).toBeLessThan(after.indexOf("Fresh"));
  });

  it("paces a turn opened by its own first text (no send ahead of it)", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscriptPage").mockResolvedValue(page([]));
    const source = await mountPaced(client);

    act(() => void source.text(REPLY));
    expect(liveText()).not.toContain("one");
    tick(0);
    tick(100);
    expect(liveText()).toContain("one");
    expect(liveText()).not.toContain("three");
  });
});
