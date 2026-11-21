import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { resetEventsBusForTests } from "./events";
import { resetMeshForTests } from "./mesh";
import { recordSessionHosts } from "./sessionHosts";
import { FakeEventSource } from "./testFakeEventSource";
import type { RouterClient } from "./client";
import type { Session } from "./types";
import { useTaskStore } from "../tasks/useTaskStore";
import { useOpenDelegateRecords } from "../features/sessions/state/useOpenDelegateRecords";

// Mesh mode, at the callers: a remote session's REST read waits for its
// host's bus (eventsReady(sessionId)), not this device's.

const remote = (id: string) => ({ id, host: { name: "pop-os", self: false, reachable: true } }) as Session;

/** Holds GET /peers/pop-os/events/head; this device's head answers at once. */
function holdPeerHead() {
  let answer!: (r: Response) => void;
  const held = new Promise<Response>((r) => (answer = r));
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      const path = new URL(input).pathname;
      if (path === "/peers/pop-os/events/head") return held;
      if (path === "/events/head") return new Response(JSON.stringify({ seq: 40 }), { status: 200 });
      return new Response("[]", { status: 200 });
    }),
  );
  return async (seq: number) => {
    await act(async () => {
      answer(new Response(JSON.stringify({ seq }), { status: 200 }));
      await new Promise((r) => setTimeout(r, 0));
    });
  };
}

const settle = () =>
  act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });

describe("a remote session's reads wait for its host's bus", () => {
  beforeEach(() => {
    resetMeshForTests();
    resetEventsBusForTests();
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => {
    resetMeshForTests();
    vi.unstubAllGlobals();
  });

  it("useTaskStore reads the task list only once the peer's head is in", async () => {
    const release = holdPeerHead();
    recordSessionHosts([remote("ses_r")]);
    const listTasks = vi.fn().mockResolvedValue([]);
    const client = { listTasks } as unknown as RouterClient;
    renderHook(() => useTaskStore(client, "ses_r"));
    await settle();
    await settle();
    expect(listTasks).not.toHaveBeenCalled(); // this device's bus is ready; the peer's isn't
    await release(3);
    await settle();
    expect(listTasks).toHaveBeenCalledWith("ses_r");
  });

  it("useOpenDelegateRecords reads again when its session's host is learned", async () => {
    const release = holdPeerHead();
    const getSessionDelegates = vi.fn().mockResolvedValue([]);
    const client = { getSessionDelegates } as unknown as RouterClient;
    renderHook(() => useOpenDelegateRecords(client, { sessionId: "ses_late", harnessId: "h1" }));
    await settle();
    await settle();
    expect(getSessionDelegates).toHaveBeenCalledTimes(1); // on this device's bus, host unknown
    recordSessionHosts([remote("ses_late")]);
    await settle();
    expect(getSessionDelegates).toHaveBeenCalledTimes(1); // not before the host's head
    await release(9);
    await settle();
    expect(getSessionDelegates).toHaveBeenCalledTimes(2);
  });
});
