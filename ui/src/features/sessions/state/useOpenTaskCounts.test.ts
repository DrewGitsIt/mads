import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useOpenTaskCounts } from "./useOpenTaskCounts";
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

describe("useOpenTaskCounts", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("counts only open-status tasks per session", async () => {
    const tasks = [
      makeTask({ id: "t1", session_id: "s1", status: "ready" }),
      makeTask({ id: "t2", session_id: "s1", status: "done" }),
      makeTask({ id: "t3", session_id: "s2", status: "in_progress" }),
    ];
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(tasks), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useOpenTaskCounts());

    await waitFor(() => expect(result.current).toEqual({ s1: 1, s2: 1 }));
  });

  it("refetches on a task.status_changed event", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify([makeTask({ id: "t1", session_id: "s1", status: "ready" })]), {
          status: 200,
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify([makeTask({ id: "t1", session_id: "s1", status: "done" })]), {
          status: 200,
        }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useOpenTaskCounts());
    await waitFor(() => expect(result.current).toEqual({ s1: 1 }));

    const source = FakeEventSource.instances[0];
    act(() => {
      source.emit(
        "task.status_changed",
        JSON.stringify({ seq: 1, type: "task.status_changed", actor: "user", task_id: "t1" }),
      );
    });

    await waitFor(() => expect(result.current).toEqual({}));
  });
});
