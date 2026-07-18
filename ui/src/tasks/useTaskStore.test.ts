import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { useTaskStore } from "./useTaskStore";
import { FakeRouterClient, fixtureTasks } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import type { Envelope } from "../router/types";

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
});
