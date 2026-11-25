// @vitest-environment jsdom
// Same jsdom rationale as ConversationTab.test.tsx: ChatRenderer renders
// markdown through DOMPurify, which doesn't sanitize under happy-dom.
//
// The chat pane on the keyed conversation (docs/CHAT-STREAM-DESIGN.md §5,
// §6): one keyed stream, rows merged by key, no wholesale refetches, the
// send's own user_message as its bubble, failed sends in place, the
// terminal view on transcript pages, and scroll-up paging.
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, act, within } from "@testing-library/react";
import { ConversationTab } from "./ConversationTab";
import { FakeRouterClient } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import { requestStreamReattach } from "./streamReattachSignal";
import { frames, page } from "./keyed/testFrames";
import { holdEventsHead } from "../router/testEventsBarrier";

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

let nextSeq = 1;
/** A control-plane envelope on whichever subscription asked for its type. */
function controlEnvelope(type: string, payload: unknown) {
  const source = FakeEventSource.instances.find((s) => s.url.includes(type));
  expect(source).toBeTruthy();
  act(() => {
    source!.emit(type, JSON.stringify({ seq: nextSeq++, type, actor: "system", session_id: "session-active", payload }));
  });
}

/** Control-plane envelopes delivered together, with no render between them. */
function controlBatch(events: [type: string, payload: unknown][]) {
  act(() => {
    for (const [type, payload] of events) {
      const source = FakeEventSource.instances.find((s) => s.url.includes(type));
      expect(source).toBeTruthy();
      source!.emit(type, JSON.stringify({ seq: nextSeq++, type, actor: "system", session_id: "session-active", payload }));
    }
  });
}

async function mount(client: FakeRouterClient) {
  render(<ConversationTab sessionId="session-active" routerClient={client} />);
  await waitFor(() => expect(client.v2Sources.length).toBeGreaterThan(0));
  await waitFor(() => expect(screen.getByPlaceholderText(/message the agent/i)).toBeTruthy());
  return frames(client);
}

const stream = () => screen.getByTestId("conversation-stream");

describe("ConversationTab — keyed conversation", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("opens one keyed stream and no legacy one in chat", async () => {
    const client = new FakeRouterClient();
    await mount(client);
    expect(client.v2Sources).toHaveLength(1);
    expect(client.v2Sources[0].after).toBeUndefined();
    expect(client.sessionEventSources).toHaveLength(0);
  });

  it("takes the working indicator and the queue from hello", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscriptPage").mockResolvedValue(page([]));
    const source = await mount(client);
    act(() => {
      source.hello({
        turn: { active: true, started_at: new Date().toISOString() },
        queued: [{ id: "q1", text: "and then the docs", queued_at: new Date().toISOString() }],
      });
      source.replayEnd();
    });
    expect(screen.getByRole("status", { name: "Working…" })).toBeTruthy();
    expect(screen.getByText("and then the docs")).toBeTruthy();

    // queue_changed keeps the list current; its delivery invents no bubble —
    // the delivered message's own user_message frame is the bubble.
    controlEnvelope("session.queue_changed", { queued: [], delivered: { id: "q1", text: "and then the docs" } });
    expect(screen.queryByText("and then the docs")).toBeNull();
    act(() => void source.user("and then the docs", { queued: true }));
    expect(screen.getAllByText("and then the docs")).toHaveLength(1);
  });

  // A resumed connection can bring queue changes from before the window's
  // notice epoch; the queue folds them, but an old failure is not reported.
  it("folds an old queue_changed but reports only a failure since the window's first mount", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscriptPage").mockResolvedValue(page([]));
    await mount(client);
    const source = FakeEventSource.instances.find((s) => s.url.includes("session.queue_changed"))!;
    const send = (timestamp: string, text: string) =>
      act(() => {
        source.emit(
          "session.queue_changed",
          JSON.stringify({
            seq: nextSeq++,
            timestamp,
            type: "session.queue_changed",
            actor: "system",
            session_id: "session-active",
            payload: { queued: [{ id: "q1", text, queued_at: timestamp }], failed: { id: "q0", text: "x" }, error: "boom" },
          }),
        );
      });
    send("2020-01-01T00:00:00Z", "queued long ago");
    expect(screen.getByText("queued long ago")).toBeTruthy();
    expect(screen.queryByText(/Couldn't send your queued message/)).toBeNull();

    send(new Date(Date.now() + 1000).toISOString(), "queued just now");
    await waitFor(() => expect(screen.getByText(/Couldn't send your queued message: boom/)).toBeTruthy());
  });

  // A surface round trip: the tab unmounts, the bus resumes on return and
  // delivers a failure from while it was gone. The queue shows the message
  // gone either way; without the banner it would read as sent. The gate is
  // the window's notice epoch, not this mount.
  it("reports a queued-message failure that happened during a tab trip", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscriptPage").mockResolvedValue(page([]));
    const firstMount = render(<ConversationTab sessionId="session-active" routerClient={client} />);
    await waitFor(() => expect(screen.getByPlaceholderText(/message the agent/i)).toBeTruthy());
    firstMount.unmount();
    const duringTrip = new Date().toISOString();
    await new Promise((r) => setTimeout(r, 5));

    FakeEventSource.instances = [];
    await mount(client);
    const source = FakeEventSource.instances.find((s) => s.url.includes("session.queue_changed"))!;
    act(() => {
      source.emit(
        "session.queue_changed",
        JSON.stringify({
          seq: nextSeq++,
          timestamp: duringTrip,
          type: "session.queue_changed",
          actor: "system",
          session_id: "session-active",
          payload: { queued: [], failed: { id: "q0", text: "x" }, error: "boom" },
        }),
      );
    });
    await waitFor(() => expect(screen.getByText(/Couldn't send your queued message: boom/)).toBeTruthy());
  });

  // The ready barrier (router/events.ts eventsReady): the record, the event
  // history, the delegates and the queue merge with live bus events, so none
  // is read until the bus knows its cursor.
  it("reads the record, history, delegates and queue only after the bus's head", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscriptPage").mockResolvedValue(page([]));
    const reads = {
      session: vi.spyOn(client, "getSession"),
      history: vi.spyOn(client, "getEvents"),
      delegates: vi.spyOn(client, "getSessionDelegates"),
      queue: vi.spyOn(client, "getQueue"),
    };
    const head = holdEventsHead();
    render(<ConversationTab sessionId="session-active" routerClient={client} />);
    await waitFor(() => expect(client.v2Sources.length).toBeGreaterThan(0));
    for (const read of Object.values(reads)) expect(read).not.toHaveBeenCalled();

    await head.release(5);
    await waitFor(() => {
      for (const read of Object.values(reads)) expect(read).toHaveBeenCalled();
    });
    expect(reads.history).toHaveBeenCalledWith(expect.objectContaining({ sessionId: "session-active" }));
  });

  describe("the queue seed (the bus never replays queue_changed)", () => {
    const q = (id: string, text: string) => ({ id, text, queued_at: new Date().toISOString() });

    it("seeds the queue from GET /sessions/{id}/queue when nothing newer has spoken", async () => {
      const client = new FakeRouterClient();
      vi.spyOn(client, "getTranscriptPage").mockResolvedValue(page([]));
      const getQueue = vi.spyOn(client, "getQueue").mockResolvedValue([q("q9", "queued before I looked")]);
      await mount(client);
      await waitFor(() => expect(screen.getByText("queued before I looked")).toBeTruthy());
      expect(getQueue).toHaveBeenCalledWith("session-active");
    });

    it("drops the seed when a live queue_changed spoke first (live wins)", async () => {
      const client = new FakeRouterClient();
      vi.spyOn(client, "getTranscriptPage").mockResolvedValue(page([]));
      let answer!: (v: ReturnType<typeof q>[]) => void;
      vi.spyOn(client, "getQueue").mockReturnValue(new Promise((r) => (answer = r)));
      await mount(client);
      controlEnvelope("session.queue_changed", { queued: [q("q2", "the live queue")] });
      await act(async () => answer([q("q9", "stale snapshot")]));
      expect(screen.queryByText("stale snapshot")).toBeNull();
      expect(screen.getByText("the live queue")).toBeTruthy();
    });

    it("drops the seed when hello brought the queue first — never applied twice", async () => {
      const client = new FakeRouterClient();
      vi.spyOn(client, "getTranscriptPage").mockResolvedValue(page([]));
      let answer!: (v: ReturnType<typeof q>[]) => void;
      vi.spyOn(client, "getQueue").mockReturnValue(new Promise((r) => (answer = r)));
      const source = await mount(client);
      act(() => {
        source.hello({ queued: [q("q1", "from hello")] });
        source.replayEnd();
      });
      await act(async () => answer([q("q1", "from hello"), q("q0", "older snapshot")]));
      expect(screen.getAllByText("from hello")).toHaveLength(1);
      expect(screen.queryByText("older snapshot")).toBeNull();
    });
  });

  it("never duplicates a row: settled, the turn-end page, a reattach, a refocus and an inject all merge by key", async () => {
    const client = new FakeRouterClient();
    const getPage = vi.spyOn(client, "getTranscriptPage").mockResolvedValue(page([]));
    const source = await mount(client);

    let user = "";
    let reply = "";
    act(() => {
      user = source.user("ship it");
      reply = source.text("shipped");
      source.settled([user, reply]);
    });
    const stored = page([
      { role: "user", key: user, text: "ship it", timestamp: "2026-10-21T10:00:00Z" },
      { role: "agent", key: reply, text: "shipped", timestamp: "2026-10-21T10:00:01Z" },
    ]);
    getPage.mockResolvedValue(stored);
    act(() => source.turnEnd());
    await waitFor(() => expect(getPage).toHaveBeenCalledTimes(2));

    // A refocus fetches nothing.
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
    act(() => void document.dispatchEvent(new Event("visibilitychange")));
    // An inject arrives as its own user_message, not a refetch.
    controlEnvelope("link.message_injected", {});
    expect(getPage).toHaveBeenCalledTimes(2);

    // A reattach that resyncs swaps in the last page and replays the tail.
    act(() => requestStreamReattach("session-active"));
    await waitFor(() => expect(client.v2Sources).toHaveLength(2));
    const second = frames(client);
    await act(async () => {
      second.hello({ resume: "resync" });
      second.frame("resync", { reason: "epoch" });
      second.replayEnd();
      await Promise.resolve();
    });
    await waitFor(() => expect(getPage).toHaveBeenCalledTimes(3));

    expect(screen.getAllByText("ship it")).toHaveLength(1);
    expect(screen.getAllByText("shipped")).toHaveLength(1);
  });

  it("shows a send the stream reported unsaved as not delivered, with the send's error and Retry", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscriptPage").mockResolvedValue(page([]));
    const send = vi.spyOn(client, "sendMessage").mockRejectedValueOnce(new Error("agent unreachable"));
    const source = await mount(client);

    fireEvent.change(screen.getByPlaceholderText(/message the agent/i), { target: { value: "deploy" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(screen.getByText("Couldn't send your message")).toBeTruthy());
    // The failed send takes back the working indicator it showed.
    expect(screen.queryByRole("status", { name: "Working…" })).toBeNull();

    act(() => {
      const key = source.user("deploy");
      source.unsaved([key]);
    });
    const failed = stream().querySelector("[data-undelivered]");
    expect(failed?.textContent).toContain("Not delivered");
    expect(failed?.textContent).toContain("agent unreachable");

    send.mockResolvedValueOnce({ status: "accepted" });
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(send).toHaveBeenLastCalledWith("session-active", { text: "deploy", mentions: [], queue: true }));
    // The retry's own frame folds the failed try into it.
    act(() => void source.user("deploy"));
    expect(within(stream()).getAllByText("deploy")).toHaveLength(1);
    expect(screen.getByRole("button", { name: "1 failed attempt" })).toBeTruthy();
  });

  it("renders stored thinking and notices in place, and keeps a subagent's rows out of the main list", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscriptPage").mockResolvedValue(
      page([
        { role: "user", key: "u1", text: "look into it" },
        { role: "thinking", key: "th1", text: "the cache key misses the tenant" },
        { role: "agent", key: "a1", text: "Found it." },
        { role: "agent", key: "n1", text: "[Request interrupted by user]", notice: true },
      ]),
    );
    const source = await mount(client);
    await waitFor(() => expect(screen.getByText("Found it.")).toBeTruthy());
    expect(screen.getByTestId("activity-group").textContent).toMatch(/1 action/);
    expect(screen.getByRole("note").textContent).toContain("[Request interrupted by user]");

    act(() => void source.text("a subagent's own words", "tu_sub"));
    expect(screen.queryByText("a subagent's own words")).toBeNull();
  });

  it("loads the page before the oldest row when scrolled near the top", async () => {
    const client = new FakeRouterClient();
    const getPage = vi
      .spyOn(client, "getTranscriptPage")
      .mockResolvedValueOnce(page([{ role: "user", key: "u5", text: "the latest question" }], true))
      .mockResolvedValueOnce(page([{ role: "user", key: "u4", text: "an older question" }], false));
    await mount(client);
    await waitFor(() => expect(screen.getByText("the latest question")).toBeTruthy());

    fireEvent.scroll(stream());
    await waitFor(() => expect(screen.getByText("an older question")).toBeTruthy());
    expect(getPage.mock.calls[1]).toEqual(["session-active", { before: "u5", limit: 100 }]);
    const order = [...stream().querySelectorAll("[data-user-msg]")].map((el) => el.textContent);
    expect(order[0]).toContain("an older question");

    // No more pages: scrolling asks for nothing.
    fireEvent.scroll(stream());
    expect(getPage).toHaveBeenCalledTimes(2);
  });

  it("Cmd+↑↑ loads every older page as one, with a note while it does, then lands on the first message", async () => {
    const client = new FakeRouterClient();
    let release!: () => void;
    const held = new Promise<void>((r) => (release = r));
    const getPage = vi
      .spyOn(client, "getTranscriptPage")
      .mockResolvedValueOnce(page([{ role: "user", key: "u5", text: "the latest question" }], true))
      .mockResolvedValueOnce(page([{ role: "user", key: "u4", text: "an older question" }], true))
      .mockImplementationOnce(async () => {
        await held;
        return page([{ role: "user", key: "u1", text: "the very first question" }], false);
      });
    Element.prototype.scrollTo ??= function () {};
    await mount(client);
    await waitFor(() => expect(screen.getByText("the latest question")).toBeTruthy());
    stream().scrollTop = 500;

    fireEvent.keyDown(window, { key: "ArrowUp", metaKey: true });
    fireEvent.keyDown(window, { key: "ArrowUp", metaKey: true });
    await waitFor(() => expect(screen.getByTestId("loading-all").textContent).toBe("Loading earlier messages…"));
    // Nothing merges page by page: the first older page waits for the rest.
    expect(screen.queryByText("an older question")).toBeNull();

    await act(async () => release());
    await waitFor(() => expect(screen.getByText("the very first question")).toBeTruthy());
    expect(screen.queryByTestId("loading-all")).toBeNull();
    expect(getPage.mock.calls[2]).toEqual(["session-active", { before: "u4", limit: 100 }]);
    const order = [...stream().querySelectorAll("[data-user-msg]")].map((el) => el.textContent);
    expect(order[0]).toContain("the very first question");
    expect(order[1]).toContain("an older question");
    expect(stream().scrollTop).toBe(0);
  });

  it("Cmd+↑↑ is called off by reading on: nothing merges", async () => {
    const client = new FakeRouterClient();
    let release!: () => void;
    const held = new Promise<void>((r) => (release = r));
    vi.spyOn(client, "getTranscriptPage")
      .mockResolvedValueOnce(page([{ role: "user", key: "u5", text: "the latest question" }], true))
      .mockImplementationOnce(async () => {
        await held;
        return page([{ role: "user", key: "u4", text: "an older question" }], false);
      });
    Element.prototype.scrollTo ??= function () {};
    await mount(client);
    await waitFor(() => expect(screen.getByText("the latest question")).toBeTruthy());

    fireEvent.keyDown(window, { key: "ArrowUp", metaKey: true });
    fireEvent.keyDown(window, { key: "ArrowUp", metaKey: true });
    await waitFor(() => expect(screen.getByTestId("loading-all")).toBeTruthy());
    fireEvent.wheel(stream());
    expect(screen.queryByTestId("loading-all")).toBeNull();
    await act(async () => release());
    expect(screen.queryByText("an older question")).toBeNull();
  });

  it("a turn the stream already ended stays ended when a stale working arrives with its end", async () => {
    // A send the harness refuses at once (an expired login): the stream's
    // turn_end lands before the control plane's working / turn_ended / idle,
    // which then arrive in one batch.
    const client = new FakeRouterClient();
    const getPage = vi.spyOn(client, "getTranscriptPage").mockResolvedValue(page([]));
    const source = await mount(client);
    act(() => {
      source.hello();
      source.replayEnd();
    });
    let user = "";
    act(() => {
      user = source.user("hello there");
    });
    getPage.mockResolvedValue(
      page([
        { role: "user", key: user, text: "hello there", timestamp: "2026-11-06T00:05:08Z" },
        {
          role: "agent",
          key: "a1",
          text: "Failed to authenticate: OAuth session expired",
          is_error: true,
          timestamp: "2026-11-06T00:05:08Z",
        },
      ]),
    );
    act(() => source.turnEnd());
    const since = new Date().toISOString();
    controlBatch([
      ["session.agent_state", { state: "working", since }],
      ["session.turn_ended", { delta: "…" }],
      ["session.agent_state", { state: "idle", since }],
    ]);

    expect(screen.queryByRole("status", { name: "Working…" })).toBeNull();
    await waitFor(() => expect(within(stream()).getByRole("button", { name: "Retry" })).toBeTruthy());
  });

  it("reads transcript pages only in the terminal view, again at each turn's end", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTransportMode").mockResolvedValue({ mode: "terminal" });
    const getPage = vi.spyOn(client, "getTranscriptPage").mockResolvedValue(page([]));
    render(<ConversationTab sessionId="session-active" routerClient={client} />);
    await waitFor(() => expect(screen.getByTestId("session-dock").getAttribute("data-mode")).toBe("terminal"));
    // The PTY rides the legacy stream; the keyed one is closed.
    await waitFor(() => expect(client.sessionEventSources.length).toBeGreaterThan(0));
    expect(client.v2Sources.every((s) => s.source.closed)).toBe(true);
    const before = getPage.mock.calls.length;

    controlEnvelope("session.turn_ended", { delta: "…" });
    await waitFor(() => expect(getPage.mock.calls.length).toBe(before + 1));
  });
});
