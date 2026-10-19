import { afterEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { PipelineRun, Template } from "../../router/types";
import { RunBar, type RunBarLiveProps, type RunBarPastProps, type RunBarPlanProps } from "./RunBar";

function run(seq: number, outcome: PipelineRun["outcome"], started_at: string, ended_at?: string): PipelineRun {
  return {
    session_id: "s1",
    run_seq: seq,
    started_at,
    ended_at,
    outcome,
    tasks_done: 0,
    tasks_blocked: 0,
    tasks_skipped: 0,
    cost_usd: 0,
  };
}

// Newest first, as the history hook returns them. run_seq values are event
// seqs, not ordinals: the bar must number them 1..3 oldest-first.
const RUNS: PipelineRun[] = [
  run(90, "running", "2026-10-19T12:00:00Z"),
  run(40, "completed", "2026-10-17T12:00:00Z", "2026-10-17T12:22:00Z"),
  run(12, "failed", "2026-10-15T12:00:00Z", "2026-10-15T12:01:00Z"),
];

const TEMPLATES: Template[] = [
  {
    id: "tpl-default",
    name: "default-implementation",
    description: "Design, implement, build, review, merge, verify.",
    version: 3,
    created_at: "",
    updated_at: "",
    source: [
      "name: default-implementation",
      "stages:",
      "  - name: executor",
      "    agent: executor",
      "  - name: build",
      "    command: make build",
      "    on_fail: { loop_back: executor, max_loops: 2 }",
    ].join("\n"),
  },
  {
    id: "tpl-quick",
    name: "quick-fix",
    description: "No design or review pass.",
    version: 1,
    created_at: "",
    updated_at: "",
    source: ["name: quick-fix", "stages:", "  - name: executor", "    agent: executor"].join("\n"),
  },
];

const common = {
  view: "list" as const,
  onViewChange: () => {},
  runs: RUNS,
  onSelectRun: () => {},
};

function planProps(over: Partial<RunBarPlanProps> = {}): RunBarPlanProps {
  return {
    ...common,
    mode: "plan",
    templates: TEMPLATES,
    templateId: "tpl-default",
    defaultTemplateId: "tpl-default",
    onTemplateChange: () => {},
    runnableCount: 4,
    onStart: async () => {},
    ...over,
  };
}

function liveProps(over: Partial<RunBarLiveProps> = {}): RunBarLiveProps {
  return {
    ...common,
    mode: "live",
    selectedRunSeq: 90,
    templateLabel: "default-implementation v3",
    startedAt: "2026-10-19T12:00:00Z",
    onCancel: async () => {},
    ...over,
  };
}

function pastProps(over: Partial<RunBarPastProps> = {}): RunBarPastProps {
  return {
    ...common,
    mode: "past",
    selectedRunSeq: 40,
    outcome: "completed",
    templateLabel: "default-implementation v3",
    startedAt: "2026-10-17T12:00:00Z",
    endedAt: "2026-10-17T12:22:05Z",
    onNewRun: () => {},
    ...over,
  };
}

afterEach(() => {
  vi.useRealTimers();
});

describe("RunBar: plan", () => {
  it("shows New run, the template, and Run N tasks; no Cancel", () => {
    render(<RunBar {...planProps()} />);
    expect(screen.getByRole("radio", { name: "List" }).getAttribute("aria-checked")).toBe("true");
    expect(screen.getByRole("button", { name: /New run/ }).getAttribute("aria-expanded")).toBe("false");
    expect(screen.getByRole("button", { name: "Template: default-implementation" })).toBeTruthy();
    expect((screen.getByRole("button", { name: "Run 4 tasks" }) as HTMLButtonElement).disabled).toBe(false);
    expect(screen.queryByRole("button", { name: /Cancel/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Pause/ })).toBeNull();
  });

  it("disables Run at 0 runnable tasks and says singular at 1", () => {
    const { rerender } = render(<RunBar {...planProps({ runnableCount: 0 })} />);
    expect((screen.getByRole("button", { name: "Run 0 tasks" }) as HTMLButtonElement).disabled).toBe(true);
    rerender(<RunBar {...planProps({ runnableCount: 1 })} />);
    expect((screen.getByRole("button", { name: "Run 1 task" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("shows one warning line with its full text as the title", () => {
    const warning = "Refresh rotation won't run (depends on TokenStore)";
    render(<RunBar {...planProps({ warning })} />);
    expect(screen.getByTitle(warning).textContent).toContain(warning);
  });

  it("disables Run while starting and shows a start error inline", async () => {
    let reject!: (e: Error) => void;
    const onStart = vi.fn(() => new Promise<void>((_, r) => (reject = r)));
    render(<RunBar {...planProps({ onStart })} />);
    const btn = screen.getByRole("button", { name: "Run 4 tasks" });
    fireEvent.click(btn);
    expect(onStart).toHaveBeenCalledTimes(1);
    expect((btn as HTMLButtonElement).disabled).toBe(true);
    await act(async () => reject(new Error("no ready tasks")));
    expect(screen.getByRole("alert").textContent).toContain("no ready tasks");
    expect((btn as HTMLButtonElement).disabled).toBe(false);
  });

  it("template menu lists templates with default chip, version, description and stage chain", () => {
    const onTemplateChange = vi.fn();
    const onOpenLibrary = vi.fn();
    render(<RunBar {...planProps({ onTemplateChange, onOpenLibrary })} />);
    const trigger = screen.getByRole("button", { name: "Template: default-implementation" });
    fireEvent.click(trigger);
    expect(trigger.getAttribute("aria-expanded")).toBe("true");

    const menu = screen.getByRole("menu", { name: "Templates" });
    const items = within(menu).getAllByRole("menuitemradio");
    expect(items).toHaveLength(2);
    expect(items[0].getAttribute("aria-checked")).toBe("true");
    expect(items[0].textContent).toContain("workspace default");
    expect(items[0].textContent).toContain("v3");
    expect(items[0].textContent).toContain("Design, implement, build, review, merge, verify.");
    expect(within(items[0]).getByLabelText("Stages: executor, build")).toBeTruthy();
    expect(within(items[0]).getByTitle("on_fail: loop_back executor ×2")).toBeTruthy();
    expect(items[1].textContent).not.toContain("workspace default");
    expect(screen.getByText("Templates live in Library. A run keeps a frozen copy.")).toBeTruthy();

    fireEvent.click(items[1]);
    expect(onTemplateChange).toHaveBeenCalledWith("tpl-quick");
    expect(screen.queryByRole("menu")).toBeNull();

    fireEvent.click(trigger);
    fireEvent.click(screen.getByRole("button", { name: "Edit in Library ↗" }));
    expect(onOpenLibrary).toHaveBeenCalledTimes(1);
  });

  it("hides Edit in Library without onOpenLibrary", () => {
    render(<RunBar {...planProps()} />);
    fireEvent.click(screen.getByRole("button", { name: "Template: default-implementation" }));
    expect(screen.queryByRole("button", { name: /Edit in Library/ })).toBeNull();
  });
});

describe("RunBar: run menu", () => {
  it("lists New run then this session's runs numbered oldest-first, and selects", () => {
    vi.useFakeTimers({ now: new Date("2026-10-19T12:14:30Z") });
    const onSelectRun = vi.fn();
    render(
      <RunBar
        {...planProps({ onSelectRun, templateLabelOf: (r) => (r.run_seq === 12 ? "default-implementation v2" : undefined) })}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: /New run/ }));
    const items = within(screen.getByRole("menu", { name: "Runs" })).getAllByRole("menuitemradio");
    expect(items.map((el) => el.textContent)).toEqual([
      "+ New runchoose a template, then Run",
      "Run 3runningstarted 14 min ago",
      "Run 2completedOct 17 · 22m",
      "Run 1failedOct 15 · 1mdefault-implementation v2",
    ]);
    expect(items[0].getAttribute("aria-checked")).toBe("true");
    expect(document.activeElement).toBe(items[0]);

    fireEvent.click(items[2]);
    expect(onSelectRun).toHaveBeenCalledWith(40);

    fireEvent.click(screen.getByRole("button", { name: /New run/ }));
    fireEvent.click(screen.getAllByRole("menuitemradio")[0]);
    expect(onSelectRun).toHaveBeenLastCalledWith("new");
  });

  it("moves focus with the arrow keys and Escape returns to the trigger", () => {
    render(<RunBar {...pastProps()} />);
    const trigger = screen.getByRole("button", { name: /Run 2/ });
    fireEvent.click(trigger);
    const items = screen.getAllByRole("menuitemradio");
    expect(document.activeElement).toBe(items[2]); // the shown run
    fireEvent.keyDown(items[2], { key: "ArrowDown" });
    expect(document.activeElement).toBe(items[3]);
    fireEvent.keyDown(items[3], { key: "ArrowDown" });
    expect(document.activeElement).toBe(items[0]);
    fireEvent.keyDown(items[0], { key: "End" });
    expect(document.activeElement).toBe(items[3]);
    fireEvent.keyDown(items[3], { key: "Escape" });
    expect(screen.queryByRole("menu")).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });
});

describe("RunBar: live", () => {
  it("shows Run N with a running chip, template, ticking elapsed, and Cancel run", () => {
    vi.useFakeTimers({ now: new Date("2026-10-19T12:14:27Z") });
    render(<RunBar {...liveProps()} />);
    const trigger = screen.getByRole("button", { name: /Run 3/ });
    expect(trigger.textContent).toContain("running");
    expect(screen.getByText("default-implementation v3")).toBeTruthy();
    expect(screen.getByLabelText("Elapsed 14:27")).toBeTruthy();
    act(() => {
      vi.advanceTimersByTime(2000);
    });
    expect(screen.getByLabelText("Elapsed 14:29")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Cancel run" }) as HTMLButtonElement).disabled).toBe(false);
    expect(screen.queryByRole("button", { name: /Template/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Run \d+ task/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Pause/ })).toBeNull();
  });

  it("numbers a live run the history hasn't caught up with as the next one", () => {
    render(<RunBar {...liveProps({ runs: RUNS.slice(1), selectedRunSeq: 90 })} />);
    expect(screen.getByRole("button", { name: /Run 3/ })).toBeTruthy();
  });

  it("disables Cancel with a reason, and shows a cancel error inline", async () => {
    const { rerender } = render(<RunBar {...liveProps({ cancelDisabledReason: "This run has no run id" })} />);
    const cancel = screen.getByRole("button", { name: "Cancel run" });
    expect((cancel as HTMLButtonElement).disabled).toBe(true);
    expect(cancel.getAttribute("title")).toBe("This run has no run id");

    const onCancel = vi.fn().mockRejectedValue(new Error("cancel failed"));
    rerender(<RunBar {...liveProps({ onCancel })} />);
    fireEvent.click(screen.getByRole("button", { name: "Cancel run" }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("cancel failed"));
  });
});

describe("RunBar: past", () => {
  it("shows Run N with its outcome, template, duration, and New run", () => {
    const onNewRun = vi.fn();
    render(<RunBar {...pastProps({ onNewRun })} />);
    expect(screen.getByRole("button", { name: /Run 2/ }).textContent).toContain("completed");
    expect(screen.getByLabelText("Took 22:05")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Cancel run" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "New run" }));
    expect(onNewRun).toHaveBeenCalledTimes(1);
  });

  it("shows no clock for a run with no recorded end", () => {
    render(<RunBar {...pastProps({ outcome: "interrupted", endedAt: undefined })} />);
    expect(screen.queryByLabelText(/Took/)).toBeNull();
    expect(screen.getByRole("button", { name: /Run 2/ }).textContent).toContain("interrupted");
  });

  it("switches view", () => {
    const onViewChange = vi.fn();
    render(<RunBar {...pastProps({ onViewChange })} />);
    fireEvent.click(screen.getByRole("radio", { name: "Graph" }));
    expect(onViewChange).toHaveBeenCalledWith("graph");
  });
});
