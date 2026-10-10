// @vitest-environment jsdom
// Same jsdom rationale as ConversationTab.test.tsx: ChatRenderer renders
// markdown through DOMPurify, which doesn't sanitize under happy-dom.
//
// A paced (words-per-minute) reveal across the turn's end: the agent's
// burst plays out at the chosen pace, the composer frees up as soon as the
// agent is done, Show all skips ahead, and a send mid-reveal hands the rest
// of the reply to the history ahead of the new message.
//
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react";
import { ConversationTab } from "./ConversationTab";
import { FakeRouterClient, fixtureConfig } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";

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
let frames: FrameRequestCallback[] = [];
let now = 0;
function tick(ms: number) {
  act(() => {
    now += ms;
    const run = frames;
    frames = [];
    for (const cb of run) cb(now);
  });
}

const REPLY = "one two three four five six seven eight nine ten eleven twelve.";

async function mountPaced(client: FakeRouterClient) {
  vi.spyOn(client, "getConfig").mockResolvedValue({ ...fixtureConfig, stream_speed: "custom", stream_wpm: 600 });
  render(<ConversationTab sessionId="session-active" routerClient={client} />);
  await waitFor(() => expect(client.sessionEventSources.length).toBeGreaterThan(0));
  await waitFor(() => expect(screen.getByPlaceholderText(/message the agent/i)).toBeTruthy());
  // Let the pace load before anything streams.
  await act(async () => {
    await Promise.resolve();
  });
  return client.sessionEventSources.at(-1)!;
}

function liveText(): string {
  return screen.getByTestId("conversation-stream").textContent ?? "";
}

describe("ConversationTab — paced reveal", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    frames = [];
    now = 0;
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      frames.push(cb);
      return frames.length;
    });
    vi.stubGlobal("cancelAnimationFrame", () => {
      frames = [];
    });
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("plays a burst out at the chosen pace, past the turn's end, with the composer free", async () => {
    const client = new FakeRouterClient();
    const transcript = vi.spyOn(client, "getTranscript").mockResolvedValue([]);
    const source = await mountPaced(client);

    act(() => {
      source.emit({ kind: "text", text: REPLY });
      source.emit({ kind: "turn_end" });
    });
    tick(0); // the loop's first frame sets its clock
    tick(100); // 600 wpm = 10 words/s → one word
    expect(liveText()).toContain("one");
    expect(liveText()).not.toContain("three");

    // The agent is done: no Stop, the send button sends, and nothing has
    // reconciled away the reveal yet.
    expect(screen.queryByRole("button", { name: "Stop generating" })).toBeNull();
    expect(screen.getByRole("button", { name: "Send" })).toBeTruthy();
    expect(transcript).toHaveBeenCalledTimes(1); // the mount's own load

    // 1.1s of reading left — under the two-second mark, so no Show all.
    expect(screen.queryByRole("button", { name: "Show all" })).toBeNull();
    for (let i = 0; i < 12; i++) tick(100);
    expect(liveText()).toContain("twelve.");
    // Drained: the turn reconciles with the transcript once.
    await waitFor(() => expect(transcript).toHaveBeenCalledTimes(2));
  });

  it("Show all reveals the rest at once", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscript").mockResolvedValue([]);
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
    vi.spyOn(client, "getTranscript").mockResolvedValue([]);
    const source = await mountPaced(client);

    act(() => {
      source.emit({ kind: "text", text: REPLY });
      source.emit({ kind: "turn_end" });
    });
    tick(0);
    tick(100);

    const composer = screen.getByPlaceholderText(/message the agent/i);
    fireEvent.change(composer, { target: { value: "next question" } });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Send" }));
      await Promise.resolve();
    });

    const all = liveText();
    expect(all).toContain("twelve.");
    expect(all.indexOf("twelve.")).toBeLessThan(all.indexOf("next question"));
  });

  it("a late 'working' from the control plane doesn't cut a reveal short", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscript").mockResolvedValue([]);
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

  it("the next turn's first frame hands a still-revealing reply to the history", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscript").mockResolvedValue([]);
    const source = await mountPaced(client);

    act(() => {
      source.emit({ kind: "text", text: REPLY });
      source.emit({ kind: "turn_end" });
    });
    tick(0);
    tick(100);
    // A turn this pane didn't send (a linked session's inject) starts.
    act(() => {
      source.emit({ kind: "text", text: "Fresh reply begins. " });
    });
    const all = liveText();
    expect(all).toContain("twelve.");
    tick(0); // the new turn's loop sets its clock
    tick(100);
    const after = liveText();
    expect(after).toContain("Fresh");
    expect(after.indexOf("twelve.")).toBeLessThan(after.indexOf("Fresh"));
  });
});
