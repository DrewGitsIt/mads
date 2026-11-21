import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useOpenTaskCounts } from "./useOpenTaskCounts";
import { resetEventsBusForTests } from "../../../router/events";
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

  it("counts only open-status tasks per session, bucketed by running/ready/blocked", async () => {
    const tasks = [
      makeTask({ id: "t1", session_id: "s1", status: "ready" }),
      makeTask({ id: "t2", session_id: "s1", status: "done" }),
      makeTask({ id: "t3", session_id: "s2", status: "in_progress" }),
      makeTask({ id: "t4", session_id: "s2", status: "blocked" }),
      // needs_review folds into the ready bucket (same simplification as graphStatus.ts).
      makeTask({ id: "t5", session_id: "s2", status: "needs_review" }),
    ];
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(tasks), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useOpenTaskCounts());

    await waitFor(() =>
      expect(result.current).toEqual({
        s1: { running: 0, ready: 1, blocked: 0 },
        s2: { running: 1, ready: 1, blocked: 1 },
      }),
    );
  });

  // The bus never replays: a task that changed after the mount read but
  // before the bus read its head would never be heard. So the mount read
  // waits for the head, and sees that change itself.
  it("reads after the bus's head, so a change landing between subscribe and head is still counted", async () => {
    resetEventsBusForTests(); // the production first connect: head unknown
    let head!: (r: Response) => void;
    let server = [makeTask({ id: "t1", session_id: "s1", status: "ready" })];
    const fetchMock = vi.fn((url: string) =>
      new URL(url).pathname === "/events/head"
        ? new Promise<Response>((r) => (head = r))
        : Promise.resolve(new Response(JSON.stringify(server), { status: 200 })),
    );
    vi.stubGlobal("fetch", fetchMock);
    const tasksCalls = () => fetchMock.mock.calls.filter(([url]) => new URL(url).pathname === "/tasks").length;

    const { result } = renderHook(() => useOpenTaskCounts());
    await act(async () => {});
    expect(tasksCalls()).toBe(0);

    // t1 starts running (task.status_changed seq 4) before the head is read:
    // the stream starts after it and never delivers it.
    server = [makeTask({ id: "t1", session_id: "s1", status: "in_progress" })];
    await act(async () => head(new Response('{"seq":4}', { status: 200 })));
    await waitFor(() => expect(result.current).toEqual({ s1: { running: 1, ready: 0, blocked: 0 } }));
    expect(tasksCalls()).toBe(1);
    expect(new URL(FakeEventSource.instances[0].url).searchParams.get("since")).toBe("4");
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
    await waitFor(() => expect(result.current).toEqual({ s1: { running: 0, ready: 1, blocked: 0 } }));

    const source = FakeEventSource.instances[0];
    act(() => {
      source.emit(
        "task.status_changed",
        JSON.stringify({ seq: 1, type: "task.status_changed", actor: "user", task_id: "t1" }),
      );
    });

    await waitFor(() => expect(result.current).toEqual({}));
  });

  it("moves a task from ready to running on a task.status_changed event (engine claim)", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify([makeTask({ id: "t1", session_id: "s1", status: "ready" })]), {
          status: 200,
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify([makeTask({ id: "t1", session_id: "s1", status: "in_progress" })]), {
          status: 200,
        }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useOpenTaskCounts());
    await waitFor(() => expect(result.current).toEqual({ s1: { running: 0, ready: 1, blocked: 0 } }));

    const source = FakeEventSource.instances[0];
    act(() => {
      source.emit(
        "task.status_changed",
        JSON.stringify({ seq: 1, type: "task.status_changed", actor: "user", task_id: "t1" }),
      );
    });

    await waitFor(() => expect(result.current).toEqual({ s1: { running: 1, ready: 0, blocked: 0 } }));
  });
});
