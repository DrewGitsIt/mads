import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useAllTasks } from "./useAllTasks";
import { FakeEventSource } from "../../../router/testFakeEventSource";
import type { Task } from "../../../router/types";

function makeTask(overrides: Partial<Task>): Task {
  return {
    id: "t1",
    session_id: "s1",
    status: "ready",
    retry_count: 0,
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    source: "user",
    title: "task",
    ...overrides,
  };
}

describe("useAllTasks", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fetches every task, unfiltered by session", async () => {
    const tasks = [
      makeTask({ id: "t1", session_id: "s1", status: "ready" }),
      makeTask({ id: "t2", session_id: "s2", status: "done" }),
    ];
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(tasks), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useAllTasks());

    await waitFor(() => expect(result.current).toHaveLength(2));
    expect(fetchMock.mock.calls[0][0].toString()).not.toContain("session_id");
  });

  it("refetches on a task.created event", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify([]), { status: 200 }))
      .mockResolvedValueOnce(
        new Response(JSON.stringify([makeTask({ id: "t1" })]), { status: 200 }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useAllTasks());
    await waitFor(() => expect(result.current).toHaveLength(0));

    const source = FakeEventSource.instances[0];
    act(() => {
      source.emit(
        "task.created",
        JSON.stringify({ seq: 1, type: "task.created", actor: "user", task_id: "t1" }),
      );
    });

    await waitFor(() => expect(result.current).toHaveLength(1));
  });
});
