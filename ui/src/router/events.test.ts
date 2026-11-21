import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  EVENTS_NOT_READY_MESSAGE,
  eventsReady,
  eventsReadyWithin,
  getBusHealth,
  getEventsCursorForTests,
  resetEventsBusForTests,
  subscribeToBusHealth,
  subscribeToEvents,
} from "./events";
import { FakeEventSource } from "./testFakeEventSource";
import type { Envelope } from "./types";

function envelope(seq: number, type = "session.created"): Envelope {
  return { seq, timestamp: "2026-07-15T00:00:00Z", type, actor: "user", session_id: "s1" };
}

/**
 * A fetch that answers GET /events/head with the bus's own cursor (the log
 * unchanged — what a reconnect after a drop rechecks) and anything else
 * with `rest`.
 */
function withHead(rest: (url: string) => Promise<Response> = async () => new Response("[]", { status: 200 })) {
  return vi.fn((url: string) =>
    new URL(url).pathname === "/events/head"
      ? Promise.resolve(new Response(JSON.stringify({ seq: getEventsCursorForTests() ?? 0 }), { status: 200 }))
      : rest(url),
  );
}

describe("subscribeToEvents", () => {
  const unsubscribes: (() => void)[] = [];

  function subscribe(...args: Parameters<typeof subscribeToEvents>) {
    const unsubscribe = subscribeToEvents(...args);
    unsubscribes.push(unsubscribe);
    return unsubscribe;
  }

  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    vi.stubGlobal("fetch", withHead());
  });

  afterEach(() => {
    // A window-singleton bus tears itself down once its last subscriber
    // unsubscribes, so a test that doesn't unsubscribe would otherwise leak
    // its connection into the next test (setupTests resets the cursor).
    for (const unsubscribe of unsubscribes.splice(0)) unsubscribe();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("builds the /events URL with the union of subscribed types and since=<cursor>", () => {
    subscribe({ types: ["session.created", "task.created"] }, () => {});

    const source = FakeEventSource.instances[0];
    const url = new URL(source.url);
    expect(url.pathname).toBe("/events");
    expect(url.searchParams.get("since")).toBe("0");
    expect(url.searchParams.get("types")).toBe("session.created,task.created");
  });

  it("dispatches a scripted frame to onEvent", () => {
    const onEvent = vi.fn();
    subscribe({ types: ["session.created"] }, onEvent);

    const source = FakeEventSource.instances[0];
    const env = envelope(1);
    source.emit("session.created", JSON.stringify(env));

    expect(onEvent).toHaveBeenCalledWith(env);
  });

  it("only dispatches for subscribed types", () => {
    const onEvent = vi.fn();
    subscribe({ types: ["session.created"] }, onEvent);

    const source = FakeEventSource.instances[0];
    source.emit("task.created", JSON.stringify(envelope(1, "task.created")));

    expect(onEvent).not.toHaveBeenCalled();
  });

  it("closes the EventSource on unsubscribe", () => {
    const unsubscribe = subscribe({ types: ["session.created"] }, () => {});
    const source = FakeEventSource.instances[0];

    expect(source.closed).toBe(false);
    unsubscribe();
    expect(source.closed).toBe(true);
  });

  it("shares one EventSource across subscribers with identical params, closing only once all unsubscribe", () => {
    const first = vi.fn();
    const second = vi.fn();
    const unsubscribeFirst = subscribe({ types: ["session.created"] }, first);
    const unsubscribeSecond = subscribe({ types: ["session.created"] }, second);

    expect(FakeEventSource.instances).toHaveLength(1);
    const source = FakeEventSource.instances[0];

    const env = envelope(1);
    source.emit("session.created", JSON.stringify(env));
    expect(first).toHaveBeenCalledWith(env);
    expect(second).toHaveBeenCalledWith(env);

    unsubscribeFirst();
    expect(source.closed).toBe(false);
    unsubscribeSecond();
    expect(source.closed).toBe(true);
  });

  it("holds a single open connection for subscribers with different (non-overlapping) types, each receiving only its own types", () => {
    const first = vi.fn();
    const second = vi.fn();
    subscribe({ types: ["session.created"] }, first);
    subscribe({ types: ["task.created"] }, second);

    const open = FakeEventSource.instances.filter((s) => !s.closed);
    expect(open).toHaveLength(1);
    const source = open[0];

    source.emit("session.created", JSON.stringify(envelope(1, "session.created")));
    source.emit("task.created", JSON.stringify(envelope(2, "task.created")));

    expect(first).toHaveBeenCalledTimes(1);
    expect(first).toHaveBeenCalledWith(expect.objectContaining({ type: "session.created" }));
    expect(second).toHaveBeenCalledTimes(1);
    expect(second).toHaveBeenCalledWith(expect.objectContaining({ type: "task.created" }));
  });

  it("widens by reconnecting from the scalar cursor — the new type gets no history", () => {
    subscribe({ types: ["session.created"] }, () => {});
    const first = FakeEventSource.instances[0];
    first.emit("session.created", JSON.stringify(envelope(5, "session.created")));

    subscribe({ types: ["task.created"] }, () => {});

    expect(FakeEventSource.instances).toHaveLength(2);
    expect(first.closed).toBe(true);
    const url = new URL(FakeEventSource.instances[1].url);
    expect(url.searchParams.get("types")).toBe("session.created,task.created");
    expect(url.searchParams.get("since")).toBe("5");
  });

  it("dedupes on the scalar cursor: anything at or below it is skipped, whatever its type", () => {
    const second = vi.fn();
    subscribe({ types: ["session.created"] }, () => {});
    FakeEventSource.instances[0].emit("session.created", JSON.stringify(envelope(10, "session.created")));
    subscribe({ types: ["task.created"] }, second);
    const source = FakeEventSource.instances[1];
    source.emit("task.created", JSON.stringify(envelope(3, "task.created")));
    source.emit("task.created", JSON.stringify(envelope(11, "task.created")));
    expect(second).toHaveBeenCalledTimes(1);
    expect(second).toHaveBeenCalledWith(expect.objectContaining({ seq: 11 }));
  });

  it("delivers every seq exactly once across a dropped connection resumed via cursor", async () => {
    vi.useFakeTimers();
    const onEvent = vi.fn();
    subscribe({ types: ["session.created"] }, onEvent);

    const first = FakeEventSource.instances[0];
    first.emit("session.created", JSON.stringify(envelope(1)));
    expect(onEvent).toHaveBeenCalledTimes(1);

    first.simulateError();
    expect(first.closed).toBe(true);
    await vi.advanceTimersByTimeAsync(30_000); // clears any backoff/jitter delay (and the head recheck)

    expect(FakeEventSource.instances).toHaveLength(2);
    const second = FakeEventSource.instances[1];
    expect(new URL(second.url).searchParams.get("since")).toBe("1");

    // The overlap edge: the resumed connection may redeliver seq 1 — it must
    // be deduped, not counted again.
    second.emit("session.created", JSON.stringify(envelope(1)));
    second.emit("session.created", JSON.stringify(envelope(2)));

    expect(onEvent).toHaveBeenCalledTimes(2);
    expect(onEvent).toHaveBeenNthCalledWith(1, expect.objectContaining({ seq: 1 }));
    expect(onEvent).toHaveBeenNthCalledWith(2, expect.objectContaining({ seq: 2 }));
  });

  it("drops and reconnects rather than growing the dispatch queue unbounded, delivering every seq exactly once after", async () => {
    vi.useFakeTimers();
    const received: number[] = [];
    let triggered = false;
    subscribe({ types: ["session.created"] }, (env) => {
      received.push(env.seq);
      if (triggered) return;
      triggered = true;
      // Simulates a burst arriving reentrantly while this very envelope is
      // still being handled — exactly the scenario the bounded queue guards
      // against, since draining is line-rate but MAX_QUEUE (500) caps backlog.
      const source = FakeEventSource.instances[0];
      for (let seq = 2; seq <= 502; seq++) {
        source.emit("session.created", JSON.stringify(envelope(seq)));
      }
    });

    const first = FakeEventSource.instances[0];
    first.emit("session.created", JSON.stringify(envelope(1)));

    // Only seq 1 was ever dispatched — the reentrant burst overflowed the
    // queue before any of it drained, so it was dropped, not delivered.
    expect(received).toEqual([1]);
    expect(first.closed).toBe(true);

    await vi.advanceTimersByTimeAsync(30_000);
    expect(FakeEventSource.instances).toHaveLength(2);
    const second = FakeEventSource.instances[1];
    expect(new URL(second.url).searchParams.get("since")).toBe("1");

    second.emit("session.created", JSON.stringify(envelope(2)));
    second.emit("session.created", JSON.stringify(envelope(3)));
    expect(received).toEqual([1, 2, 3]);
  });

  it("backs off after a connect error, then reconnects", async () => {
    vi.useFakeTimers();
    vi.stubGlobal("fetch", withHead());
    subscribe({ types: ["session.created"] }, () => {});
    FakeEventSource.instances[0].simulateError();
    await vi.advanceTimersByTimeAsync(0);
    expect(FakeEventSource.instances).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(FakeEventSource.instances).toHaveLength(2);
  });

  it("reports failing after repeated connect errors, and live again once data flows", async () => {
    vi.useFakeTimers();
    vi.stubGlobal("fetch", withHead());
    const seen: string[] = [];
    const stop = subscribeToBusHealth((h) => seen.push(h.state));
    subscribe({ types: ["session.created"] }, () => {});

    FakeEventSource.instances[0].simulateError();
    expect(getBusHealth().state).not.toBe("failing"); // one drop is ordinary
    await vi.advanceTimersByTimeAsync(30_000);
    FakeEventSource.instances[1].simulateError();
    expect(getBusHealth().state).toBe("failing");

    await vi.advanceTimersByTimeAsync(30_000);
    FakeEventSource.instances[2].emit("session.created", JSON.stringify(envelope(1)));
    expect(getBusHealth().state).toBe("live");
    expect(seen).toEqual(["failing", "live"]);
    stop();
  });

  it("keeps the cursor across teardown — a Retro→Sessions round trip resumes, never replays", () => {
    const unsubscribe = subscribe({ types: ["session.created"] }, () => {});
    FakeEventSource.instances[0].emit("session.created", JSON.stringify(envelope(9)));
    unsubscribe();
    unsubscribes.pop();
    expect(FakeEventSource.instances[0].closed).toBe(true);

    subscribe({ types: ["session.created", "task.created"] }, () => {});
    expect(new URL(FakeEventSource.instances[1].url).searchParams.get("since")).toBe("9");
  });

  describe("first connect (no cursor yet)", () => {
    function stubHead(seq: number) {
      const fetchMock = vi.fn(async (url: string) => {
        if (new URL(url).pathname === "/events/head") return new Response(JSON.stringify({ seq }), { status: 200 });
        return new Response("[]", { status: 200 });
      });
      vi.stubGlobal("fetch", fetchMock);
      return fetchMock;
    }

    beforeEach(() => {
      resetEventsBusForTests(); // undefined cursor: the production first-connect path
    });

    it("reads the log head and connects from it, never from the beginning", async () => {
      const fetchMock = stubHead(42);
      subscribe({ types: ["session.created"] }, () => {});
      expect(FakeEventSource.instances).toHaveLength(0);
      await vi.waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
      expect(new URL(FakeEventSource.instances[0].url).searchParams.get("since")).toBe("42");
      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(getEventsCursorForTests()).toBe(42);
    });

    it("folds subscribes made while the head is pending into one connection", async () => {
      stubHead(7);
      subscribe({ types: ["session.created"] }, () => {});
      subscribe({ types: ["task.created"] }, () => {});
      await vi.waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
      const url = new URL(FakeEventSource.instances[0].url);
      expect(url.searchParams.get("types")).toBe("session.created,task.created");
      expect(url.searchParams.get("since")).toBe("7");
    });

    it("resumes a drop from the cursor even when nothing was delivered yet", async () => {
      stubHead(42);
      vi.useFakeTimers();
      subscribe({ types: ["session.created"] }, () => {});
      await vi.advanceTimersByTimeAsync(0);
      FakeEventSource.instances[0].simulateError();
      await vi.advanceTimersByTimeAsync(30_000);
      expect(FakeEventSource.instances).toHaveLength(2);
      expect(new URL(FakeEventSource.instances[1].url).searchParams.get("since")).toBe("42");
    });

    it("never connects from the start of the log (since=0) once the head is past it, across head, widen, drop and teardown", async () => {
      stubHead(3);
      vi.useFakeTimers();
      const un = subscribe({ types: ["session.created"] }, () => {});
      await vi.advanceTimersByTimeAsync(0);
      FakeEventSource.instances[0].emit("session.created", JSON.stringify(envelope(4)));
      subscribe({ types: ["task.created"] }, () => {});
      FakeEventSource.instances.at(-1)!.simulateError();
      await vi.advanceTimersByTimeAsync(30_000);
      un();
      unsubscribes.splice(unsubscribes.indexOf(un), 1);
      for (const u of unsubscribes.splice(0)) u();
      subscribe({ types: ["session.created"] }, () => {});
      for (const es of FakeEventSource.instances) {
        const since = new URL(es.url).searchParams.get("since");
        expect(since).not.toBeNull();
        expect(Number(since)).toBeGreaterThan(0);
      }
      expect(FakeEventSource.instances.length).toBeGreaterThanOrEqual(4);
    });

    it("retries the head after a failed read, with backoff", async () => {
      vi.useFakeTimers();
      let calls = 0;
      vi.stubGlobal(
        "fetch",
        vi.fn(async () => (++calls === 1 ? new Response("", { status: 502 }) : new Response('{"seq":8}', { status: 200 }))),
      );
      subscribe({ types: ["session.created"] }, () => {});
      await vi.advanceTimersByTimeAsync(0);
      expect(FakeEventSource.instances).toHaveLength(0);
      await vi.advanceTimersByTimeAsync(30_000);
      expect(FakeEventSource.instances).toHaveLength(1);
      expect(new URL(FakeEventSource.instances[0].url).searchParams.get("since")).toBe("8");
    });

    it("drops a head that lands after teardown", async () => {
      stubHead(5);
      const un = subscribe({ types: ["session.created"] }, () => {});
      un();
      unsubscribes.pop();
      await new Promise((r) => setTimeout(r, 0));
      expect(FakeEventSource.instances).toHaveLength(0);
    });

    it("reports failing from the first failed head read while nothing is ready yet, and keeps retrying", async () => {
      vi.useFakeTimers();
      const error = vi.spyOn(console, "error").mockImplementation(() => {});
      const info = vi.spyOn(console, "info").mockImplementation(() => {});
      let down = true;
      let heads = 0;
      vi.stubGlobal(
        "fetch",
        vi.fn(async () => {
          heads++;
          if (down) throw new TypeError("Failed to fetch");
          return new Response('{"seq":5}', { status: 200 });
        }),
      );
      let readied = false;
      void eventsReady().then(() => (readied = true));
      subscribe({ types: ["session.created"] }, () => {});
      await vi.advanceTimersByTimeAsync(0);
      expect(getBusHealth().state).toBe("failing"); // views are waiting: say so at once
      await vi.advanceTimersByTimeAsync(120_000);
      expect(heads).toBeGreaterThan(2);
      expect(readied).toBe(false);
      expect(FakeEventSource.instances).toHaveLength(0);
      expect(error).toHaveBeenCalledTimes(1); // once on the way in, not per retry
      expect(info).not.toHaveBeenCalled();

      down = false;
      await vi.advanceTimersByTimeAsync(30_000);
      expect(readied).toBe(true);
      expect(FakeEventSource.instances).toHaveLength(1);
      expect(new URL(FakeEventSource.instances[0].url).searchParams.get("since")).toBe("5");
      expect(info).toHaveBeenCalledTimes(1);
      expect(error).toHaveBeenCalledTimes(1);
    });

    it("keeps the two-miss grace for failed head reads once the cursor is known", async () => {
      vi.useFakeTimers();
      let heads = 0;
      vi.stubGlobal(
        "fetch",
        vi.fn(async () => (++heads === 1 ? new Response('{"seq":3}', { status: 200 }) : new Response("", { status: 503 }))),
      );
      subscribe({ types: ["session.created"] }, () => {});
      await vi.advanceTimersByTimeAsync(0);
      FakeEventSource.instances[0].simulateError(); // first miss: the drop
      expect(getBusHealth().state).toBe("connecting");
      await vi.advanceTimersByTimeAsync(30_000); // the recheck's head read fails: second miss
      expect(getBusHealth().state).toBe("failing");
    });

    it("eventsReadyWithin rejects after its timeout while the head can't be read, and resolves once it is", async () => {
      vi.useFakeTimers();
      vi.spyOn(console, "error").mockImplementation(() => {});
      vi.spyOn(console, "info").mockImplementation(() => {});
      let down = true;
      vi.stubGlobal(
        "fetch",
        vi.fn(async () => (down ? new Response("", { status: 502 }) : new Response('{"seq":1}', { status: 200 }))),
      );
      subscribe({ types: ["session.created"] }, () => {});
      const first = eventsReadyWithin(10_000);
      const outcome = first.then(
        () => "ready",
        (err: Error) => err.message,
      );
      await vi.advanceTimersByTimeAsync(10_000);
      expect(await outcome).toBe(EVENTS_NOT_READY_MESSAGE);
      down = false;
      await vi.advanceTimersByTimeAsync(30_000);
      await expect(eventsReadyWithin(10)).resolves.toBeUndefined();
    });

    it.each([404, 405])("stops for good when the head answers %i — the services predate this UI", async (status) => {
      vi.useFakeTimers();
      const error = vi.spyOn(console, "error").mockImplementation(() => {});
      const fetchMock = vi.fn(async () => new Response('{"error":"not found"}', { status }));
      vi.stubGlobal("fetch", fetchMock);
      let readied = false;
      void eventsReady().then(() => (readied = true));
      subscribe({ types: ["session.created"] }, () => {});
      await vi.advanceTimersByTimeAsync(0);
      expect(getBusHealth().state).toBe("incompatible");
      expect(error).toHaveBeenCalledTimes(1);

      // No retry loop: not on the backoff, not on wake, not on a new subscribe.
      await vi.advanceTimersByTimeAsync(120_000);
      window.dispatchEvent(new Event("online"));
      subscribe({ types: ["task.created"] }, () => {});
      await vi.advanceTimersByTimeAsync(120_000);
      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(FakeEventSource.instances).toHaveLength(0);
      expect(readied).toBe(false);
      expect(error).toHaveBeenCalledTimes(1);

      // Terminal across teardown too.
      for (const u of unsubscribes.splice(0)) u();
      expect(getBusHealth().state).toBe("incompatible");
    });

    it("connects with since=0 when the head says the log is empty", async () => {
      stubHead(0);
      subscribe({ types: ["session.created"] }, () => {});
      await vi.waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
      expect(new URL(FakeEventSource.instances[0].url).searchParams.get("since")).toBe("0");
      expect(getEventsCursorForTests()).toBe(0);
    });

    it("eventsReady resolves once the head lands, and stays resolved across drop, widen and teardown", async () => {
      vi.useFakeTimers();
      let answer!: (r: Response) => void;
      let heads = 0;
      vi.stubGlobal(
        "fetch",
        vi.fn((url: string) =>
          new URL(url).pathname !== "/events/head"
            ? Promise.resolve(new Response("[]", { status: 200 }))
            : ++heads === 1
              ? new Promise<Response>((r) => (answer = r))
              : Promise.resolve(new Response('{"seq":12}', { status: 200 })),
        ),
      );
      let readied = 0;
      const un = subscribe({ types: ["session.created"] }, () => {});
      void eventsReady().then(() => readied++);
      await vi.advanceTimersByTimeAsync(0);
      expect(readied).toBe(0);

      answer(new Response('{"seq":12}', { status: 200 }));
      await vi.advanceTimersByTimeAsync(0);
      expect(readied).toBe(1);

      FakeEventSource.instances[0].simulateError();
      subscribe({ types: ["task.created"] }, () => {});
      un();
      unsubscribes.splice(unsubscribes.indexOf(un), 1);
      for (const u of unsubscribes.splice(0)) u(); // teardown
      let after = false;
      void eventsReady().then(() => (after = true));
      await vi.advanceTimersByTimeAsync(0);
      expect(after).toBe(true);
      // The cursor is known: the only head read since is the recheck the
      // drop asked for, on the next connect.
      subscribe({ types: ["session.created"] }, () => {});
      await vi.advanceTimersByTimeAsync(0);
      expect(heads).toBe(2);
      expect(readied).toBe(1);
      expect(new URL(FakeEventSource.instances.at(-1)!.url).searchParams.get("since")).toBe("12");
    });
  });

  describe("reconnect policy", () => {
    function setVisibility(state: DocumentVisibilityState) {
      Object.defineProperty(document, "visibilityState", { value: state, configurable: true });
      document.dispatchEvent(new Event("visibilitychange"));
    }

    beforeEach(() => {
      vi.useFakeTimers();
      // Every backoff draws its full window, so a pending retry is plainly pending.
      vi.spyOn(Math, "random").mockReturnValue(0.999);
      vi.stubGlobal("fetch", withHead());
    });

    afterEach(() => {
      Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
    });

    it("does not let a subscribe jump a pending backoff — it joins the retry", async () => {
      subscribe({ types: ["session.created"] }, () => {});
      FakeEventSource.instances[0].emit("session.created", JSON.stringify(envelope(4)));
      FakeEventSource.instances[0].simulateError();
      await vi.advanceTimersByTimeAsync(0);

      subscribe({ types: ["task.created"] }, () => {});
      expect(FakeEventSource.instances).toHaveLength(1);

      await vi.advanceTimersByTimeAsync(1000);
      expect(FakeEventSource.instances).toHaveLength(2);
      const url = new URL(FakeEventSource.instances[1].url);
      expect(url.searchParams.get("types")).toBe("session.created,task.created");
      expect(url.searchParams.get("since")).toBe("4");
    });

    it("reconnects at once, from the cursor, on coming back online", async () => {
      subscribe({ types: ["session.created"] }, () => {});
      FakeEventSource.instances[0].emit("session.created", JSON.stringify(envelope(6)));
      // Grow the backoff well past a second.
      for (let i = 0; i < 4; i++) {
        FakeEventSource.instances.at(-1)!.simulateError();
        await vi.advanceTimersByTimeAsync(30_000);
      }
      FakeEventSource.instances.at(-1)!.simulateError();
      const before = FakeEventSource.instances.length;

      window.dispatchEvent(new Event("online"));
      await vi.advanceTimersByTimeAsync(0); // the head recheck a drop asks for; no time passes
      expect(FakeEventSource.instances).toHaveLength(before + 1);
      expect(new URL(FakeEventSource.instances.at(-1)!.url).searchParams.get("since")).toBe("6");

      // The backoff starts over, and the cancelled retry never fires.
      FakeEventSource.instances.at(-1)!.simulateError();
      await vi.advanceTimersByTimeAsync(1000);
      expect(FakeEventSource.instances).toHaveLength(before + 2);
      await vi.advanceTimersByTimeAsync(30_000);
      expect(FakeEventSource.instances).toHaveLength(before + 2);
    });

    it("reconnects at once when the window comes back to the foreground, not when it goes away", async () => {
      subscribe({ types: ["session.created"] }, () => {});
      FakeEventSource.instances[0].simulateError();
      setVisibility("hidden");
      expect(FakeEventSource.instances).toHaveLength(1);
      setVisibility("visible");
      await vi.advanceTimersByTimeAsync(0); // the head recheck a drop asks for; no time passes
      expect(FakeEventSource.instances).toHaveLength(2);
    });

    it("leaves a connected bus alone on online/visible", () => {
      subscribe({ types: ["session.created"] }, () => {});
      window.dispatchEvent(new Event("online"));
      setVisibility("visible");
      expect(FakeEventSource.instances).toHaveLength(1);
      expect(FakeEventSource.instances[0].closed).toBe(false);
    });

    it("stops listening at zero subscribers and listens again on the next subscribe", () => {
      const add = vi.spyOn(window, "addEventListener");
      const remove = vi.spyOn(window, "removeEventListener");
      const addDoc = vi.spyOn(document, "addEventListener");
      const removeDoc = vi.spyOn(document, "removeEventListener");
      const un1 = subscribe({ types: ["session.created"] }, () => {});
      const un2 = subscribe({ types: ["task.created"] }, () => {});
      expect(add.mock.calls.filter(([t]) => t === "online")).toHaveLength(1);
      expect(addDoc.mock.calls.filter(([t]) => t === "visibilitychange")).toHaveLength(1);
      un1();
      expect(remove.mock.calls.filter(([t]) => t === "online")).toHaveLength(0);
      un2();
      unsubscribes.splice(0);
      expect(remove.mock.calls.filter(([t]) => t === "online")).toHaveLength(1);
      expect(removeDoc.mock.calls.filter(([t]) => t === "visibilitychange")).toHaveLength(1);
      expect(remove.mock.calls.find(([t]) => t === "online")?.[1]).toBe(add.mock.calls.find(([t]) => t === "online")?.[1]);

      // Nothing subscribed: waking opens nothing.
      window.dispatchEvent(new Event("online"));
      expect(FakeEventSource.instances.filter((s) => !s.closed)).toHaveLength(0);

      subscribe({ types: ["session.created"] }, () => {});
      expect(add.mock.calls.filter(([t]) => t === "online")).toHaveLength(2);
    });

    it("ignores online/visible while a head read is pending — no second head read", async () => {
      resetEventsBusForTests();
      let heads = 0;
      vi.stubGlobal(
        "fetch",
        vi.fn((url: string) =>
          new URL(url).pathname === "/events/head" ? (heads++, new Promise<Response>(() => {})) : Promise.resolve(new Response("[]")),
        ),
      );
      subscribe({ types: ["session.created"] }, () => {});
      await vi.advanceTimersByTimeAsync(0);
      window.dispatchEvent(new Event("online"));
      setVisibility("visible");
      await vi.advanceTimersByTimeAsync(0);
      expect(heads).toBe(1);
      expect(FakeEventSource.instances).toHaveLength(0);
    });

    // Deliberate: a widen is the subscriber asking for a type now, so it
    // reopens at once even while the open connection hasn't said "open"
    // yet — it is not a retry, so there is no backoff to respect.
    it("widens at once even while the open connection is still connecting", () => {
      subscribe({ types: ["session.created"] }, () => {});
      const connecting = FakeEventSource.instances[0]; // never emitted "open"
      subscribe({ types: ["task.created"] }, () => {});
      expect(connecting.closed).toBe(true);
      expect(FakeEventSource.instances).toHaveLength(2);
      expect(new URL(FakeEventSource.instances[1].url).searchParams.get("types")).toBe("session.created,task.created");
    });

    it("re-reads the head at once on coming back online after a failed head read", async () => {
      resetEventsBusForTests();
      let calls = 0;
      vi.stubGlobal(
        "fetch",
        vi.fn(async () => (++calls === 1 ? new Response("", { status: 502 }) : new Response('{"seq":3}', { status: 200 }))),
      );
      subscribe({ types: ["session.created"] }, () => {});
      await vi.advanceTimersByTimeAsync(0);
      expect(FakeEventSource.instances).toHaveLength(0);
      window.dispatchEvent(new Event("online"));
      await vi.advanceTimersByTimeAsync(0);
      expect(FakeEventSource.instances).toHaveLength(1);
      expect(new URL(FakeEventSource.instances[0].url).searchParams.get("since")).toBe("3");
    });
  });

  // React runs the old session's effect cleanup before the new session's
  // effect: the subscriber union shrinks (no reconnect) and then the same
  // types come back. That must not count as a widen.
  it("does not reconnect when a cleanup is followed by re-adding the same types (session switch)", () => {
    subscribe({ types: ["session.updated"] }, () => {}); // the rail, session-agnostic
    const unsubOld = subscribe({ sessionId: "s1", types: ["turn.started", "turn.ended"] }, () => {});
    expect(FakeEventSource.instances).toHaveLength(2); // a genuine widen
    const source = FakeEventSource.instances[1];

    unsubOld();
    subscribe({ sessionId: "s2", types: ["turn.started", "turn.ended"] }, () => {});

    expect(FakeEventSource.instances).toHaveLength(2);
    expect(source.closed).toBe(false);
  });

  it("still reconnects when a re-add brings a type the open connection doesn't carry", () => {
    subscribe({ types: ["session.updated"] }, () => {});
    const unsubOld = subscribe({ sessionId: "s1", types: ["turn.started"] }, () => {});
    const source = FakeEventSource.instances[1];

    unsubOld();
    subscribe({ sessionId: "s2", types: ["turn.started", "task.created"] }, () => {});

    expect(source.closed).toBe(true);
    expect(FakeEventSource.instances).toHaveLength(3);
    expect(new URL(FakeEventSource.instances[2].url).searchParams.get("types")?.split(",").sort()).toEqual([
      "session.updated",
      "task.created",
      "turn.started",
    ]);
  });
  describe("resilience", () => {
    it("resumes from the log's end when, after a drop, the log turns out to have been reset below the cursor", async () => {
      vi.useFakeTimers();
      let head = 40;
      vi.stubGlobal(
        "fetch",
        vi.fn((url: string) =>
          new URL(url).pathname === "/events/head"
            ? Promise.resolve(new Response(JSON.stringify({ seq: head })))
            : Promise.resolve(new Response("[]")),
        ),
      );
      const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
      const onEvent = vi.fn();
      subscribe({ types: ["session.created"] }, onEvent);
      FakeEventSource.instances[0].emit("session.created", JSON.stringify(envelope(40)));

      head = 3; // the Data Service came back with a fresh log
      FakeEventSource.instances[0].simulateError();
      await vi.advanceTimersByTimeAsync(30_000);
      const resumed = FakeEventSource.instances.at(-1)!;
      expect(new URL(resumed.url).searchParams.get("since")).toBe("3");
      resumed.emit("session.created", JSON.stringify(envelope(4)));
      expect(onEvent).toHaveBeenLastCalledWith(expect.objectContaining({ seq: 4 }));
      expect(warn).toHaveBeenCalledTimes(1);

      // Once only, however many times it happens.
      head = 1;
      resumed.simulateError();
      await vi.advanceTimersByTimeAsync(30_000);
      expect(new URL(FakeEventSource.instances.at(-1)!.url).searchParams.get("since")).toBe("1");
      expect(warn).toHaveBeenCalledTimes(1);
    });

    it("keeps the cursor when the log only moved on during a drop", async () => {
      vi.useFakeTimers();
      vi.stubGlobal(
        "fetch",
        vi.fn((url: string) =>
          Promise.resolve(new Response(new URL(url).pathname === "/events/head" ? '{"seq":90}' : "[]")),
        ),
      );
      subscribe({ types: ["session.created"] }, () => {});
      FakeEventSource.instances[0].emit("session.created", JSON.stringify(envelope(8)));
      FakeEventSource.instances[0].simulateError();
      await vi.advanceTimersByTimeAsync(30_000);
      expect(new URL(FakeEventSource.instances.at(-1)!.url).searchParams.get("since")).toBe("8");
    });

    it("gives up on a head read after 10 s, as a failed connect, and retries", async () => {
      resetEventsBusForTests();
      vi.useFakeTimers();
      let heads = 0;
      vi.stubGlobal(
        "fetch",
        vi.fn((url: string, init?: RequestInit) => {
          if (new URL(url).pathname !== "/events/head") return Promise.resolve(new Response("[]"));
          if (++heads > 1) return Promise.resolve(new Response('{"seq":5}'));
          // Hangs until aborted.
          return new Promise<Response>((_, reject) =>
            init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError"))),
          );
        }),
      );
      subscribe({ types: ["session.created"] }, () => {});
      await vi.advanceTimersByTimeAsync(9_999);
      expect(FakeEventSource.instances).toHaveLength(0);
      await vi.advanceTimersByTimeAsync(1);
      expect(heads).toBe(1);
      await vi.advanceTimersByTimeAsync(30_000);
      expect(heads).toBe(2);
      expect(FakeEventSource.instances).toHaveLength(1);
      expect(new URL(FakeEventSource.instances[0].url).searchParams.get("since")).toBe("5");
    });

    it("drops what the old connection queued when a subscriber widens mid-drain — the new one redelivers it, and nothing of the new type is skipped", () => {
      const sessions: number[] = [];
      const tasks: number[] = [];
      let widened = false;
      subscribe({ types: ["session.created"] }, (env) => {
        sessions.push(env.seq);
        if (widened) return;
        widened = true;
        // A burst arrives while this is handled, and the handler widens.
        FakeEventSource.instances[0].emit("session.created", JSON.stringify(envelope(12)));
        subscribe({ types: ["task.created"] }, (e) => tasks.push(e.seq));
      });
      FakeEventSource.instances[0].emit("session.created", JSON.stringify(envelope(10)));
      const fresh = FakeEventSource.instances[1];
      expect(new URL(fresh.url).searchParams.get("since")).toBe("10");
      // The new connection resumes from 10: task 11 is owed and must arrive.
      fresh.emit("task.created", JSON.stringify(envelope(11, "task.created")));
      fresh.emit("session.created", JSON.stringify(envelope(12)));
      expect(tasks).toEqual([11]);
      expect(sessions).toEqual([10, 12]);
    });
  });
});
