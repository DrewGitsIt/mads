import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { RunPicker } from "./RunPicker";
import type { PipelineRun } from "../../router/types";

function makeRun(overrides: Partial<PipelineRun>): PipelineRun {
  return {
    session_id: "ses_1",
    run_seq: 1,
    started_at: "2026-07-15T05:30:12.265401682Z",
    ended_at: "2026-07-15T05:30:12.28113183Z",
    outcome: "completed",
    tasks_done: 0,
    tasks_blocked: 0,
    tasks_skipped: 0,
    cost_usd: 0,
    ...overrides,
  };
}

function openMenu() {
  fireEvent.click(screen.getByRole("button", { name: "Run history" }));
}

describe("RunPicker", () => {
  it("renders options newest-first", () => {
    const runs = [
      makeRun({ run_seq: 100, outcome: "completed" }),
      makeRun({ run_seq: 7, outcome: "incomplete" }),
    ];
    render(<RunPicker runs={runs} selectedRunSeq={100} onSelect={vi.fn()} />);
    openMenu();

    const options = screen.getAllByRole("button", { name: /Run · started/ });
    expect(options.map((o) => o.textContent)).toEqual([
      expect.stringContaining("completed"),
      expect.stringContaining("incomplete"),
    ]);
  });

  it("fires onSelect with the chosen run's run_seq", () => {
    const runs = [makeRun({ run_seq: 100 }), makeRun({ run_seq: 7, outcome: "incomplete" })];
    const onSelect = vi.fn();
    render(<RunPicker runs={runs} selectedRunSeq={100} onSelect={onSelect} />);
    openMenu();

    fireEvent.click(screen.getByRole("button", { name: /incomplete/ }));
    expect(onSelect).toHaveBeenCalledWith(7);
  });

  it("shows a done/blocked count for a terminal run", () => {
    const run = makeRun({ run_seq: 7, outcome: "incomplete", tasks_done: 4, tasks_blocked: 1 });
    render(<RunPicker runs={[run]} selectedRunSeq={7} onSelect={vi.fn()} />);

    expect(screen.getByRole("button", { name: "Run history" }).textContent).toContain(
      "4 done / 1 blocked",
    );
  });

  it("labels a 'running' run 'in progress' with no done/blocked-implying finished claim", () => {
    const run = makeRun({ run_seq: 7, outcome: "running", ended_at: undefined });
    render(<RunPicker runs={[run]} selectedRunSeq={7} onSelect={vi.fn()} />);

    const label = screen.getByRole("button", { name: "Run history" }).textContent ?? "";
    expect(label).toContain("in progress");
    expect(label).not.toContain("completed");
    expect(label).not.toContain("incomplete");
  });
});
