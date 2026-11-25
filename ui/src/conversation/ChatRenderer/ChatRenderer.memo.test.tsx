import { createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { ChatRenderer, type ChatRendererProps } from "./ChatRenderer";
import type { PermissionRequest, ToolStatus, ToolUse } from "../../router/types";
import type { KeyedRow } from "../keyed/conversation";
import type { TaskModel } from "../activity/conversationItems";
import type { FindRequest } from "../Find/useFind";
import type { WorktreeCardModel } from "../Worktree/worktreeCards";

// What re-rendered, read off the components every item renders through:
// a message's Markdown (by its text), an open group's rows (by tool id, via
// the title hook every row calls), a task card (by its call's id).
const seen = vi.hoisted(() => ({ markdown: [] as string[], rows: [] as string[], tasks: [] as string[] }));

vi.mock("./markdown", async (importOriginal) => {
  const m = await importOriginal<typeof import("./markdown")>();
  return {
    ...m,
    Markdown: (p: Parameters<typeof m.Markdown>[0]) => {
      seen.markdown.push(p.text);
      return createElement(m.Markdown, p);
    },
  };
});

vi.mock("../activity/linkNames", async (importOriginal) => {
  const m = await importOriginal<typeof import("../activity/linkNames")>();
  return {
    ...m,
    useToolTitle: () => {
      const title = m.useToolTitle();
      return (tool: ToolUse) => {
        seen.rows.push(tool.id ?? "");
        return title(tool);
      };
    },
  };
});

vi.mock("../activity/TaskCard", async (importOriginal) => {
  const m = await importOriginal<typeof import("../activity/TaskCard")>();
  return {
    ...m,
    TaskCard: (p: Parameters<typeof m.TaskCard>[0]) => {
      seen.tasks.push(p.tool.id ?? "");
      return createElement(m.TaskCard, p);
    },
  };
});

const PERMISSION: PermissionRequest = { id: "p1", tool: "Bash", tool_use_id: "t1", input: { command: "echo t1" }, status: "pending" };

function reset() {
  seen.markdown.length = 0;
  seen.rows.length = 0;
  seen.tasks.length = 0;
}

const tool = (id: string, extra: Partial<ToolUse> = {}): ToolUse => ({
  id,
  name: "Bash",
  input: { command: `echo ${id}` },
  result: { text: `${id} out`, timestamp: "2026-11-24T10:00:02Z" },
  ...extra,
});

/** A settled history and a live turn: rows built once, so only what a test replaces is a new object. */
function conversation() {
  const rows: KeyedRow[] = [
    { key: "u1", role: "user", text: "first ask", timestamp: "2026-11-24T10:00:00Z" },
    { key: "t1", role: "tool", tool: tool("t1"), timestamp: "2026-11-24T10:00:01Z" },
    { key: "a1", role: "agent", text: "old reply", timestamp: "2026-11-24T10:00:03Z" },
    {
      key: "k1",
      role: "tool",
      tool: tool("k1", { name: "Agent", input: { description: "look around", prompt: "go" } }),
      timestamp: "2026-11-24T10:00:04Z",
    },
    { key: "u2", role: "user", text: "second ask", timestamp: "2026-11-24T10:01:00Z" },
    { key: "t2", role: "tool", tool: tool("t2"), timestamp: "2026-11-24T10:01:01Z", current: true, live: true },
    { key: "a2", role: "agent", text: "Live repl", timestamp: "2026-11-24T10:01:02Z", current: true, live: true },
  ];
  return rows;
}

/** The same rows with the live reply grown by a delta: a new object for it alone. */
function delta(rows: KeyedRow[], more: string): KeyedRow[] {
  const last = rows[rows.length - 1];
  return [...rows.slice(0, -1), { ...last, text: (last.text ?? "") + more }];
}

const task: TaskModel = { taskId: "task1", toolUseId: "k1", description: "look around", running: false, completed: true };

function setup(extra: Partial<ChatRendererProps> = {}) {
  let props: ChatRendererProps = {
    rows: conversation(),
    turnActive: true,
    tasks: new Map([["k1", task]]),
    toolStatuses: new Map(),
    onOpenTool: () => {},
    onOpenTask: () => {},
    onResolvePermission: async () => {},
    ...extra,
  };
  const view = render(<ChatRenderer {...props} />);
  // Open the settled group, so its rows render (and can be counted).
  const settled = screen.queryAllByTestId("activity-group")[0];
  if (settled) fireEvent.click(settled.querySelector("button")!);
  return {
    view,
    get props() {
      return props;
    },
    update(next: Partial<ChatRendererProps>) {
      props = { ...props, ...next };
      reset();
      view.rerender(<ChatRenderer {...props} />);
    },
  };
}

describe("ChatRenderer — a streamed delta re-renders only what it changed", () => {
  beforeEach(reset);
  afterEach(() => vi.useRealTimers());

  it("re-renders the live reply alone, not earlier messages, groups or task cards", () => {
    const c = setup();
    expect(seen.rows).toContain("t1");
    c.update({ rows: delta(c.props.rows, "y") });
    expect(seen.markdown).toEqual(["Live reply"]);
    expect(seen.rows).toEqual([]);
    expect(seen.tasks).toEqual([]);
    expect(screen.getByText("Live reply")).toBeTruthy();
    // And again, with the same callbacks and maps: still one item.
    c.update({ rows: delta(c.props.rows, " now") });
    expect(seen.markdown).toEqual(["Live reply now"]);
    expect(seen.rows).toEqual([]);
  });

  it("re-renders a joined bubble when one of its rows grows, and reuses it while none does", () => {
    const rows = conversation();
    // Two adjacent text rows join into one bubble.
    rows.push({ key: "a3", role: "agent", text: "more", current: true, live: true });
    const c = setup({ rows });
    c.update({ rows: [...rows] });
    expect(seen.markdown).toEqual([]);
    c.update({ rows: delta(rows, " text") });
    expect(seen.markdown).toEqual(["Live repl\n\nmore text"]);
  });

  it("re-renders an old group when its call's status changes, and only it", () => {
    const c = setup();
    c.update({ toolStatuses: new Map<string, ToolStatus>([["t1", "failed"]]) });
    expect(seen.rows).toEqual(["t1"]);
    expect(seen.markdown).toEqual([]);
    expect(seen.tasks).toEqual([]);
    // A status for a call no group holds re-renders nothing.
    c.update({ toolStatuses: new Map<string, ToolStatus>([["t1", "failed"], ["zz", "failed"]]) });
    expect(seen.rows).toEqual([]);
  });

  it("re-renders the task card when its model or status changes", () => {
    const c = setup();
    c.update({ tasks: new Map([["k1", { ...task, toolUses: 7 }]]) });
    expect(seen.tasks).toEqual(["k1"]);
    expect(seen.rows).toEqual([]);
    expect(screen.getByText(/7 tools/)).toBeTruthy();
    c.update({ toolStatuses: new Map<string, ToolStatus>([["k1", "blocked"]]) });
    expect(seen.tasks).toEqual(["k1"]);
  });

  it("puts a permission card arriving for an old call under its row, re-rendering only that group", () => {
    const c = setup();
    const req = PERMISSION;
    c.update({ permissionRequests: [req] });
    expect(seen.rows).toEqual(["t1"]);
    expect(seen.markdown.filter((t) => t !== "")).toEqual([]);
    const group = screen.getAllByTestId("activity-group")[0];
    expect(within(group).getByRole("button", { name: /allow/i })).toBeTruthy();
    // The same request again, in a new array: nothing changed.
    c.update({ permissionRequests: [req] });
    expect(seen.rows).toEqual([]);
  });

  it("re-renders only the groups the dock's selection moves between", () => {
    const c = setup();
    c.update({ selectedToolId: "t1" });
    expect(seen.rows).toEqual(["t1"]);
    expect(screen.getAllByTestId("activity-row").find((r) => r.hasAttribute("data-sel"))).toBeTruthy();
    c.update({ selectedToolId: "t2" });
    expect(seen.rows.sort()).toEqual(["t1", "t2"]);
    expect(screen.getAllByTestId("activity-row").filter((r) => r.hasAttribute("data-sel"))).toHaveLength(1);
    c.update({ selectedToolId: "nowhere" });
    expect(seen.rows).toEqual(["t2"]);
  });

  it("still finds and counts matches in memoized groups when a query arrives, and steps between them", () => {
    const onFindStatus = vi.fn();
    const c = setup({ onFindStatus });
    const find: FindRequest = { query: " out", matchCase: false, skipTools: false, step: 0, dir: 1 };
    c.update({ find });
    expect(onFindStatus.mock.calls.at(-1)?.[0]).toMatchObject({ count: 2, current: 0 });
    const current = () =>
      screen.getAllByTestId("activity-row").findIndex((r) => r.hasAttribute("data-find-row-current"));
    expect(current()).toBe(0);
    // The other group's row counts its hit.
    expect(screen.getByLabelText("1 match")).toBeTruthy();
    c.update({ find: { ...find, step: 1 } });
    expect(onFindStatus.mock.calls.at(-1)?.[0]).toMatchObject({ count: 2, current: 1 });
    expect(current()).toBe(1);
    c.update({ find: undefined });
    expect(screen.queryByLabelText("1 match")).toBeNull();
    expect(current()).toBe(-1);
  });

  it("folds the live group when the turn ends, and offers Retry on the latest failed send only between turns", () => {
    vi.useFakeTimers();
    const rows = conversation().slice(0, 5);
    rows[4] = { ...rows[4], failed: true };
    const onRetry = vi.fn();
    const c = setup({ rows, onRetry, sendFailure: "network down" });
    expect(screen.queryByRole("button", { name: /retry/i })).toBeNull();
    c.update({ turnActive: false });
    fireEvent.click(screen.getByRole("button", { name: /retry/i }));
    expect(onRetry).toHaveBeenCalledWith("second ask");
    // The same send, no longer the latest: Retry goes.
    c.update({ rows: [...rows, { key: "u3", role: "user", text: "third ask" }] });
    expect(screen.queryByRole("button", { name: /retry/i })).toBeNull();
  });

  it("closes the live group after the turn ends, though nothing in it changed", () => {
    vi.useFakeTimers();
    const c = setup();
    const live = () => screen.getAllByTestId("activity-group")[1];
    expect(live().hasAttribute("data-open")).toBe(true);
    c.update({ turnActive: false });
    act(() => {
      vi.advanceTimersByTime(1000);
    });
    expect(live().hasAttribute("data-open")).toBe(false);
  });

  it("re-labels stamps at midnight without any row changing: messages and Commit/Land cards", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 10, 24, 23, 59));
    const at = new Date(2026, 10, 24, 22, 30).toISOString();
    const rows: KeyedRow[] = [{ key: "a", role: "agent", text: "late reply", timestamp: at }];
    const card: WorktreeCardModel = {
      id: "c1",
      seq: 1,
      timestamp: new Date(2026, 10, 24, 22, 40).toISOString(),
      kind: "committed",
      hash: "abc1234",
      subject: "late commit",
      drafted: false,
      undone: false,
      tip: true,
    };
    const worktreeCards = [card];
    const c = setup({ rows, worktreeCards });
    const stamps = () => [...c.view.container.querySelectorAll("time")].map((t) => t.textContent!);
    expect(stamps()).toHaveLength(2);
    for (const s of stamps()) expect(s).not.toMatch(/Nov/);
    vi.setSystemTime(new Date(2026, 10, 25, 0, 1));
    c.update({ rows: [...rows] });
    for (const s of stamps()) expect(s).toMatch(/Nov/);
  });

  it("opens a collapsed group holding the approval jump's card, leaving the other groups alone", () => {
    const req = PERMISSION;
    const c = setup({ permissionRequests: [req] });
    const group = () => screen.getAllByTestId("activity-group")[0];
    // setup's click closed the group the pending card had opened.
    expect(group().hasAttribute("data-open")).toBe(false);
    c.update({ permissionReveal: { id: "p1", nonce: 1 } });
    expect(group().hasAttribute("data-open")).toBe(true);
    expect(seen.markdown).toEqual([]);
    expect(seen.rows).toEqual(["t1"]);
    // A reveal of a card no group holds re-renders none.
    c.update({ permissionReveal: { id: "elsewhere", nonce: 2 } });
    expect(seen.rows).toEqual(["t1"]); // only the group that held the last reveal
    c.update({ permissionReveal: { id: "elsewhere", nonce: 3 } });
    expect(seen.rows).toEqual([]);
  });

  it("opens the dock on a row or a task card through the stable callbacks, with the item's own ids", () => {
    const onOpenTool = vi.fn();
    const onOpenTask = vi.fn();
    const c = setup({ onOpenTool, onOpenTask });
    c.update({ rows: delta(c.props.rows, "y") });
    fireEvent.click(screen.getAllByTestId("activity-row")[0].querySelector("button")!);
    expect(onOpenTool).toHaveBeenCalledWith(expect.objectContaining({ id: "t1" }));
    fireEvent.click(screen.getByText("look around"));
    expect(onOpenTask).toHaveBeenCalledWith("k1", expect.objectContaining({ id: "k1" }));
  });
});
