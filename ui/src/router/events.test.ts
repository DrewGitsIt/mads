import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { subscribeToEvents } from "./events";
import { FakeEventSource } from "./testFakeEventSource";
import type { Envelope } from "./types";

describe("subscribeToEvents", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("builds the /events URL with since/session_id/types params", () => {
    subscribeToEvents(
      { since: 42, sessionId: "s1", types: ["session.created", "task.created"] },
      () => {},
    );

    const source = FakeEventSource.instances[0];
    const url = new URL(source.url);
    expect(url.pathname).toBe("/events");
    expect(url.searchParams.get("since")).toBe("42");
    expect(url.searchParams.get("session_id")).toBe("s1");
    expect(url.searchParams.get("types")).toBe("session.created,task.created");
  });

  it("dispatches a scripted frame to onEvent", () => {
    const onEvent = vi.fn();
    subscribeToEvents({ types: ["session.created"] }, onEvent);

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
    subscribeToEvents({ types: ["session.created"] }, onEvent);

    const source = FakeEventSource.instances[0];
    source.emit("task.created", JSON.stringify({ type: "task.created" }));

    expect(onEvent).not.toHaveBeenCalled();
  });

  it("closes the EventSource on unsubscribe", () => {
    const unsubscribe = subscribeToEvents({ types: ["session.created"] }, () => {});
    const source = FakeEventSource.instances[0];

    expect(source.closed).toBe(false);
    unsubscribe();
    expect(source.closed).toBe(true);
  });
});
