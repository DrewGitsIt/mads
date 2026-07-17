import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { subscribeToEvents } from "./events";
import { FakeEventSource } from "./testFakeEventSource";
import type { Envelope } from "./types";

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
    // Pooled connections are keyed by URL and shared across subscribers, so
    // a test that doesn't unsubscribe would otherwise leak its connection
    // into the next test (which reuses the same pool key and gets no fresh
    // FakeEventSource instance to assert against).
    for (const unsubscribe of unsubscribes.splice(0)) unsubscribe();
    vi.unstubAllGlobals();
  });

  it("builds the /events URL with since/session_id/types params", () => {
    subscribe({ since: 42, sessionId: "s1", types: ["session.created", "task.created"] }, () => {});

    const source = FakeEventSource.instances[0];
    const url = new URL(source.url);
    expect(url.pathname).toBe("/events");
    expect(url.searchParams.get("since")).toBe("42");
    expect(url.searchParams.get("session_id")).toBe("s1");
    expect(url.searchParams.get("types")).toBe("session.created,task.created");
  });

  it("dispatches a scripted frame to onEvent", () => {
    const onEvent = vi.fn();
    subscribe({ types: ["session.created"] }, onEvent);

    const source = FakeEventSource.instances[0];
    const envelope: Envelope = {
      seq: 1,
      timestamp: "2026-07-15T00:00:00Z",
      type: "session.created",
      actor: "user",
      session_id: "s1",
    };
    source.emit("session.created", JSON.stringify(envelope));

    expect(onEvent).toHaveBeenCalledWith(envelope);
  });

  it("only dispatches for subscribed types", () => {
    const onEvent = vi.fn();
    subscribe({ types: ["session.created"] }, onEvent);

    const source = FakeEventSource.instances[0];
    source.emit("task.created", JSON.stringify({ type: "task.created" }));

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

    const envelope: Envelope = {
      seq: 1,
      timestamp: "2026-07-15T00:00:00Z",
      type: "session.created",
      actor: "user",
      session_id: "s1",
    };
    source.emit("session.created", JSON.stringify(envelope));
    expect(first).toHaveBeenCalledWith(envelope);
    expect(second).toHaveBeenCalledWith(envelope);

    unsubscribeFirst();
    expect(source.closed).toBe(false);
    unsubscribeSecond();
    expect(source.closed).toBe(true);
  });

  it("opens a separate EventSource for subscribers with different params", () => {
    subscribe({ types: ["session.created"] }, () => {});
    subscribe({ types: ["task.created"] }, () => {});

    expect(FakeEventSource.instances).toHaveLength(2);
  });
});
