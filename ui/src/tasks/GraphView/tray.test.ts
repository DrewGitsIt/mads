import { describe, expect, it } from "vitest";
import { trayOrder } from "./tray";
import type { Task } from "../../router/types";

function task(overrides: Partial<Task>): Task {
  return {
    id: "tsk_1",
    session_id: "ses_1",
    status: "ready",
    retry_count: 0,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    title: "a task",
    source: "user",
    ...overrides,
  };
}

describe("trayOrder", () => {
  it("sorts by created_at ascending, independent of input array order", () => {
    const tasks = [
      task({ id: "tsk_c", created_at: "2026-07-01T12:00:00Z" }),
      task({ id: "tsk_a", created_at: "2026-07-01T09:00:00Z" }),
      task({ id: "tsk_b", created_at: "2026-07-01T10:00:00Z" }),
    ];
    expect(trayOrder(tasks).map((t) => t.id)).toEqual(["tsk_a", "tsk_b", "tsk_c"]);
  });

  it("ties by created_at break on id ascending", () => {
    const tasks = [
      task({ id: "tsk_z", created_at: "2026-07-01T09:00:00Z" }),
      task({ id: "tsk_a", created_at: "2026-07-01T09:00:00Z" }),
    ];
    expect(trayOrder(tasks).map((t) => t.id)).toEqual(["tsk_a", "tsk_z"]);
  });
});
