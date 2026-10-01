import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { getBusHealth, subscribeToBusHealth, subscribeToEvents } from "./events";
import { FakeEventSource } from "./testFakeEventSource";
import type { Envelope } from "./types";

function envelope(seq: number, type = "session.created"): Envelope {
  return { seq, timestamp: "2026-07-15T00:00:00Z", type, actor: "user", session_id: "s1" };
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
  });

  afterEach(() => {
    // A window-singleton bus tears itself down once its last subscriber
    // unsubscribes, so a test that doesn't unsubscribe would otherwise leak
    // its connection (and lastDeliveredSeq) into the next test.
    for (const unsubscribe of unsubscribes.splice(0)) unsubscribe();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("builds the /events URL with the union of subscribed types and no since param on first connect", () => {
    subscribe({ types: ["session.created", "task.created"] }, () => {});

    const source = FakeEventSource.instances[0];
    const url = new URL(source.url);
    expect(url.pathname).toBe("/events");
    expect(url.searchParams.get("since")).toBeNull();
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

  it("widens the type filter by closing the current connection and opening a new one, resuming per-type", () => {
    subscribe({ types: ["session.created"] }, () => {});
    const first = FakeEventSource.instances[0];
    first.emit("session.created", JSON.stringify(envelope(5, "session.created")));

    subscribe({ types: ["task.created"] }, () => {});

    expect(FakeEventSource.instances).toHaveLength(2);
    expect(first.closed).toBe(true);
    const second = FakeEventSource.instances[1];
    const url = new URL(second.url);
    expect(url.searchParams.get("types")).toBe("session.created,task.created");
    // The resume cursor is the *minimum* high-water-mark across the unioned
    // types, not the single scalar "highest seq ever seen": task.created has
    // no observed history on this connection, so the resume must be absent
    // (full replay of task.created, deduped against session.created's seq 5)
    // rather than "5", which would silently skip any earlier task.created
    // history that was never delivered.
    expect(url.searchParams.get("since")).toBeNull();
  });

  it("never permanently loses an earlier event of a newly-widened type, even after another type's later event advanced its own cursor", () => {
    const first = vi.fn();
    subscribe({ types: ["session.created"] }, first);
    const firstSource = FakeEventSource.instances[0];

    // session.created advances well past a task.created event this
    // connection never carried and so never delivered.
    firstSource.emit("session.created", JSON.stringify(envelope(10, "session.created")));

    const second = vi.fn();
    subscribe({ types: ["task.created"] }, second);
    expect(FakeEventSource.instances).toHaveLength(2);
    const secondSource = FakeEventSource.instances[1];

    // The earlier, never-delivered task.created event (seq 3, lower than
    // session.created's seq 10) must still reach the new subscriber — a
    // scalar cursor keyed off session.created's seq 10 would wrongly treat
    // it as already-seen and drop it forever.
    secondSource.emit("task.created", JSON.stringify(envelope(3, "task.created")));
    expect(second).toHaveBeenCalledWith(expect.objectContaining({ seq: 3, type: "task.created" }));
  });

  it("delivers every seq exactly once across a dropped connection resumed via cursor", () => {
    vi.useFakeTimers();
    const onEvent = vi.fn();
    subscribe({ types: ["session.created"] }, onEvent);

    const first = FakeEventSource.instances[0];
    first.emit("session.created", JSON.stringify(envelope(1)));
    expect(onEvent).toHaveBeenCalledTimes(1);

    first.simulateError();
    expect(first.closed).toBe(true);
    vi.advanceTimersByTime(30_000); // clears any backoff/jitter delay

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

  it("drops and reconnects rather than growing the dispatch queue unbounded, replaying every seq exactly once after", () => {
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

    vi.advanceTimersByTime(30_000);
    expect(FakeEventSource.instances).toHaveLength(2);
    const second = FakeEventSource.instances[1];
    expect(new URL(second.url).searchParams.get("since")).toBe("1");

    second.emit("session.created", JSON.stringify(envelope(2)));
    second.emit("session.created", JSON.stringify(envelope(3)));
    expect(received).toEqual([1, 2, 3]);
  });

  // Regression, 2026-09-30: a subscriber type the running Data Service hadn't
  // registered got the whole shared stream refused, and the reconnect loop
  // retried the same URL forever — every live view in the window froze.
  it("drops a type the server refuses and reconnects at once without it", async () => {
    const fetchMock = vi.fn(async (url: string) => {
      const types = new URL(url).searchParams.get("types") ?? "";
      if (!types.includes("session.queue_changed")) return new Response("[]", { status: 200 });
      return new Response(
        JSON.stringify({
          errors: [
            {
              field: "types",
              message: '"session.queue_changed" is neither a registered event type nor a domain wildcard like "task.*"',
            },
          ],
        }),
        { status: 422 },
      );
    });
    vi.stubGlobal("fetch", fetchMock);
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const onSession = vi.fn();
    subscribe({ types: ["session.updated"] }, onSession);
    subscribe({ types: ["session.queue_changed"] }, () => {});
    const refused = FakeEventSource.instances.at(-1)!;
    expect(new URL(refused.url).searchParams.get("types")).toBe("session.updated,session.queue_changed");

    refused.simulateError();
    await vi.waitFor(() => expect(FakeEventSource.instances.filter((s) => !s.closed)).toHaveLength(1));

    const retried = FakeEventSource.instances.find((s) => !s.closed)!;
    expect(retried).not.toBe(refused);
    expect(new URL(retried.url).searchParams.get("types")).toBe("session.updated");
    expect(getBusHealth().ignoredTypes).toEqual(["session.queue_changed"]);
    retried.emit("session.updated", JSON.stringify(envelope(5, "session.updated")));
    expect(onSession).toHaveBeenCalledTimes(1);
  });

  it("leaves the ordinary backoff in charge when the probe finds nothing refused", async () => {
    vi.useFakeTimers();
    vi.stubGlobal("fetch", vi.fn(async () => new Response("[]", { status: 200 })));
    subscribe({ types: ["session.created"] }, () => {});
    FakeEventSource.instances[0].simulateError();
    await vi.advanceTimersByTimeAsync(0);
    expect(FakeEventSource.instances).toHaveLength(1);
    expect(getBusHealth().ignoredTypes).toEqual([]);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(FakeEventSource.instances).toHaveLength(2);
  });

  it("reports failing after repeated connect errors, and live again once data flows", async () => {
    vi.useFakeTimers();
    vi.stubGlobal("fetch", vi.fn(async () => new Response("[]", { status: 200 })));
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

  it("resets state on teardown — a fresh subscriber after full unsubscribe gets no since param", () => {
    const unsubscribe = subscribe({ types: ["session.created"] }, () => {});
    const source = FakeEventSource.instances[0];
    source.emit("session.created", JSON.stringify(envelope(9)));
    unsubscribe();
    unsubscribes.pop(); // already unsubscribed above; don't double-unsubscribe in afterEach

    subscribe({ types: ["session.created"] }, () => {});
    const fresh = FakeEventSource.instances[1];
    expect(new URL(fresh.url).searchParams.get("since")).toBeNull();
  });
});
