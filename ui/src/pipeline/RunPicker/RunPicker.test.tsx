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

describe("RunPicker", () => {
  it("renders options newest-first", () => {
    const runs = [
      makeRun({ run_seq: 100, outcome: "completed" }),
      makeRun({ run_seq: 7, outcome: "incomplete" }),
    ];
    render(<RunPicker runs={runs} selectedRunSeq={100} onSelect={vi.fn()} />);

    const options = screen.getAllByRole("option") as HTMLOptionElement[];
    expect(options.map((o) => o.value)).toEqual(["100", "7"]);
  });

  it("fires onSelect with the chosen run's run_seq", () => {
    const runs = [makeRun({ run_seq: 100 }), makeRun({ run_seq: 7 })];
    const onSelect = vi.fn();
    render(<RunPicker runs={runs} selectedRunSeq={100} onSelect={onSelect} />);

    fireEvent.change(screen.getByRole("combobox"), { target: { value: "7" } });
    expect(onSelect).toHaveBeenCalledWith(7);
  });

  it("shows a done/blocked status line for a terminal run", () => {
    const run = makeRun({ run_seq: 7, outcome: "incomplete", tasks_done: 4, tasks_blocked: 1 });
    render(<RunPicker runs={[run]} selectedRunSeq={7} onSelect={vi.fn()} />);

    expect(screen.getByText(/Run incomplete/)).toBeTruthy();
    expect(screen.getByText(/done 4/)).toBeTruthy();
    expect(screen.getByText(/blocked 1/)).toBeTruthy();
  });

  it("omits the ended time for a dangling 'running' run", () => {
    const run = makeRun({ run_seq: 7, outcome: "running", ended_at: undefined });
    render(<RunPicker runs={[run]} selectedRunSeq={7} onSelect={vi.fn()} />);

    expect(screen.getByText(/Run started/)).toBeTruthy();
    expect(screen.queryByText(/done \d/)).toBeNull();
  });
});
