import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { useTaskStore } from "./useTaskStore";
import { FakeRouterClient, fixtureTasks } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import type { Envelope, Task } from "../router/types";

const sessionActiveTaskCount = fixtureTasks.filter((t) => t.session_id === "session-active").length;

let nextSeq = 1;
function emitTaskEvent(envelope: Omit<Envelope, "seq" | "timestamp" | "actor">) {
  const full: Envelope = { seq: nextSeq++, timestamp: new Date().toISOString(), actor: "test", ...envelope };
  FakeEventSource.instances[0].emit(full.type, JSON.stringify(full));
}

describe("useTaskStore", () => {
  beforeEach(() => {
    nextSeq = 1;
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("loads tasks for the session on mount", async () => {
    const client = new FakeRouterClient();
    const { result } = renderHook(() => useTaskStore(client, "session-active"));

    await waitFor(() => expect(result.current.tasks.length).toBe(sessionActiveTaskCount));
    expect(result.current.tasksById["tsk_ready"].title).toBe(
      "Add retry backoff to the ingest worker",
    );
  });

  it("patches a task into state on a streamed task.updated event, refetching rather than trusting the payload", async () => {
    const client = new FakeRouterClient();
    const { result } = renderHook(() => useTaskStore(client, "session-active"));
    await waitFor(() => expect(result.current.tasks.length).toBe(sessionActiveTaskCount));

    await client.updateTask({ ...result.current.tasksById["tsk_ready"], title: "Renamed via event" });
    emitTaskEvent({
      type: "task.updated",
      task_id: "tsk_ready",
      session_id: "session-active",
    });

    await waitFor(() =>
      expect(result.current.tasksById["tsk_ready"].title).toBe("Renamed via event"),
    );
  });

  it("adds a new task on a streamed task.created event", async () => {
    const client = new FakeRouterClient();
    const { result } = renderHook(() => useTaskStore(client, "session-active"));
    await waitFor(() => expect(result.current.tasks.length).toBe(sessionActiveTaskCount));

    const created = await client.createTask({
      session_id: "session-active",
      title: "Created via event",
    });
    emitTaskEvent({
      type: "task.created",
      task_id: created.id,
      session_id: "session-active",
    });

    await waitFor(() =>
      expect(result.current.tasks.some((t) => t.id === created.id)).toBe(true),
    );
  });

  it("removes a task from state on a streamed task.discarded event", async () => {
    const client = new FakeRouterClient();
    const { result } = renderHook(() => useTaskStore(client, "session-active"));
    await waitFor(() => expect(result.current.tasks.length).toBe(sessionActiveTaskCount));

    await client.discardTask("tsk_draft");
    emitTaskEvent({
      type: "task.discarded",
      task_id: "tsk_draft",
      session_id: "session-active",
    });

    await waitFor(() =>
      expect(result.current.tasks.some((t) => t.id === "tsk_draft")).toBe(false),
    );
  });

  it("never lets a snapshot requested before an event overwrite what the event changed", async () => {
    const client = new FakeRouterClient();
    const real = client.listTasks.bind(client);
    // Hold every list read; the test answers each one by hand.
    const pending: { snapshot: Task[]; answer: (t: Task[]) => void }[] = [];
    vi.spyOn(client, "listTasks").mockImplementation(async (sid) => {
      const snapshot = await real(sid);
      return new Promise<Task[]>((answer) => pending.push({ snapshot, answer }));
    });
    const { result } = renderHook(() => useTaskStore(client, "session-active"));
    await waitFor(() => expect(pending).toHaveLength(1));
    const stale = pending[0];
    expect(stale.snapshot.some((t) => t.id === "tsk_draft")).toBe(true);

    // While the mount read is in flight: a discard, and an update.
    await client.discardTask("tsk_draft");
    emitTaskEvent({ type: "task.discarded", task_id: "tsk_draft", session_id: "session-active" });
    await client.updateTask({ ...(await client.getTask("tsk_ready")), title: "Renamed mid-read" });
    emitTaskEvent({ type: "task.updated", task_id: "tsk_ready", session_id: "session-active" });
    await new Promise((r) => setTimeout(r, 80)); // past the coalesce window
    // Single-flight: the events' refetch waits for the mount read.
    expect(pending).toHaveLength(1);

    // The older snapshot lands last-requested-first: it must not resurrect
    // the discarded task.
    await act(async () => stale.answer(stale.snapshot));
    expect(result.current.tasks.some((t) => t.id === "tsk_draft")).toBe(false);

    // And the events' read follows it, so the update lands.
    await waitFor(() => expect(pending).toHaveLength(2));
    await act(async () => pending[1].answer(pending[1].snapshot));
    expect(result.current.tasksById["tsk_ready"].title).toBe("Renamed mid-read");
    expect(result.current.tasks.some((t) => t.id === "tsk_draft")).toBe(false);
  });
});
