import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { BackgroundTask } from "../../router/types";
import type { TaskModel } from "./conversationItems";
import { RunningStack } from "./RunningStack";
import type { SubstripEntry } from "./Substrip";

const minutesAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString();

function agent(n: number, startedMinutesAgo: number): SubstripEntry {
  const model: TaskModel = {
    taskId: `t${n}`,
    toolUseId: `spawn${n}`,
    description: `Wave2: agent ${n}`,
    running: true,
    completed: false,
    startedAt: minutesAgo(startedMinutesAgo),
  };
  return { model, op: `Running step ${n}` };
}

function proc(id: string, description: string, parent?: string, startedMinutesAgo = 5): BackgroundTask {
  return {
    task_id: id,
    tool_use_id: `call_${id}`,
    parent_tool_use_id: parent,
    description,
    started_at: minutesAgo(startedMinutesAgo),
  };
}

describe("RunningStack", () => {
  it("keeps a full row each for a couple of items", () => {
    render(<RunningStack agents={[agent(1, 3)]} processes={[proc("b1", "sleep 600")]} onOpen={() => {}} />);
    expect(screen.getByTestId("substrip").textContent).toContain("Wave2: agent 1");
    expect(screen.getByTestId("background-strip").textContent).toContain("sleep 600");
    expect(screen.queryByTestId("running-summary")).toBeNull();
  });

  it("folds more into one row: counts, the first subagent's step, the oldest clock", () => {
    render(
      <RunningStack
        agents={[agent(1, 31), agent(2, 7)]}
        processes={[proc("b1", "vitest run", "spawn1"), proc("b2", "until wc -l …", undefined, 45)]}
        onOpen={() => {}}
      />,
    );
    expect(screen.queryByTestId("substrip")).toBeNull();
    expect(screen.queryByTestId("background-strip")).toBeNull();
    const summary = screen.getByTestId("running-summary");
    expect(summary.textContent).toContain("2 subagents · 2 processes running");
    expect(summary.textContent).toContain("Wave2: agent 1 — Running step 1");
    expect(summary.textContent).toContain("45m");
    expect(summary.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByTestId("running-list")).toBeNull();
  });

  it("opens the list: subagents with their own processes under them, then the main agent's", () => {
    const onOpen = vi.fn();
    render(
      <RunningStack
        agents={[agent(1, 31), agent(2, 7)]}
        processes={[
          proc("b1", "vitest run", "spawn1"),
          proc("b2", "per-file results"),
          // Its subagent isn't listed (its card is on screen): not shown here.
          proc("b3", "elsewhere", "spawn9"),
        ]}
        onOpen={onOpen}
      />,
    );
    expect(screen.getByTestId("running-summary").textContent).toContain("2 subagents · 2 processes running");
    fireEvent.click(screen.getByTestId("running-summary"));
    const list = screen.getByTestId("running-list");
    const text = list.textContent ?? "";
    expect(text.indexOf("Subagents")).toBeLessThan(text.indexOf("Wave2: agent 1"));
    expect(text.indexOf("Wave2: agent 1")).toBeLessThan(text.indexOf("vitest run"));
    expect(text.indexOf("vitest run")).toBeLessThan(text.indexOf("Wave2: agent 2"));
    expect(text.indexOf("Processes")).toBeLessThan(text.indexOf("per-file results"));
    expect(text).not.toContain("elsewhere");

    fireEvent.click(within(list).getByText("Wave2: agent 2"));
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ taskId: "t2" }));

    fireEvent.click(screen.getByTestId("running-summary"));
    expect(screen.queryByTestId("running-list")).toBeNull();
  });

  it("names processes alone when no subagent runs", () => {
    render(
      <RunningStack
        agents={[]}
        processes={[proc("b1", "a"), proc("b2", "b"), proc("b3", "c")]}
        onOpen={() => {}}
      />,
    );
    expect(screen.getByTestId("running-summary").textContent).toContain("3 processes running");
  });
});
