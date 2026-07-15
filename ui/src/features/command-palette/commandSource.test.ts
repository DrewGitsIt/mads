import { describe, expect, it } from "vitest";
import { buildCommands, filterCommands, type Command } from "./commandSource";
import type { Session, Task } from "../../router/types";

function makeSession(overrides: Partial<Session>): Session {
  return {
    id: "s1",
    name: "session",
    activity: "active",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

function makeTask(overrides: Partial<Task>): Task {
  return {
    id: "t1",
    session_id: "s1",
    status: "ready",
    retry_count: 0,
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    title: "task",
    source: "user",
    ...overrides,
  };
}

describe("buildCommands", () => {
  it("emits one jump-session and one start-pipeline command per session", () => {
    const sessions = [makeSession({ id: "s1", name: "Alpha" }), makeSession({ id: "s2", name: "Beta" })];
    const commands = buildCommands(sessions, []);

    const jumps = commands.filter((c) => c.kind === "jump-session");
    const starts = commands.filter((c) => c.kind === "start-pipeline");
    expect(jumps.map((c) => c.sessionId)).toEqual(["s1", "s2"]);
    expect(starts.map((c) => c.sessionId)).toEqual(["s1", "s2"]);
  });

  it("emits one open-task command per task, labeled with the task title and session name in detail", () => {
    const sessions = [makeSession({ id: "s1", name: "Alpha" })];
    const tasks = [makeTask({ id: "t1", session_id: "s1", title: "Fix the thing" })];
    const commands = buildCommands(sessions, tasks);

    const taskCommand = commands.find((c) => c.kind === "open-task");
    expect(taskCommand).toMatchObject({
      id: "task:t1",
      label: "Fix the thing",
      detail: "Alpha",
      sessionId: "s1",
      taskId: "t1",
    });
  });

  it("appends the fixed static action commands", () => {
    const commands = buildCommands([], []);
    const kinds = commands.map((c) => c.kind);
    expect(kinds).toEqual(["new-session", "run-retro", "toggle-shell", "open-settings"]);
  });

  it("orders sessions first, then tasks, then actions", () => {
    const sessions = [makeSession({ id: "s1" })];
    const tasks = [makeTask({ id: "t1", session_id: "s1" })];
    const commands = buildCommands(sessions, tasks);
    const kinds = commands.map((c) => c.kind);
    expect(kinds).toEqual(["jump-session", "start-pipeline", "open-task", "new-session", "run-retro", "toggle-shell", "open-settings"]);
  });
});

describe("filterCommands", () => {
  const commands: Command[] = [
    { id: "1", kind: "jump-session", label: "Jump to · Alpha", detail: "mads" },
    { id: "2", kind: "jump-session", label: "Jump to · Beta", detail: "other-workspace" },
    { id: "3", kind: "new-session", label: "New session" },
  ];

  it("returns all commands for an empty query", () => {
    expect(filterCommands(commands, "")).toEqual(commands);
  });

  it("matches label case-insensitively", () => {
    expect(filterCommands(commands, "alpha").map((c) => c.id)).toEqual(["1"]);
  });

  it("matches detail as well as label", () => {
    expect(filterCommands(commands, "workspace").map((c) => c.id)).toEqual(["2"]);
  });

  it("returns an empty array when nothing matches", () => {
    expect(filterCommands(commands, "nonexistent")).toEqual([]);
  });
});
