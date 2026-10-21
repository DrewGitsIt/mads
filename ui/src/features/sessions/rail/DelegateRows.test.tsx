import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { DEFAULT_DELEGATE_FADE_MS, DelegateRows, delegateFadeMs, visibleDelegates } from "./DelegateRows";
import type { Delegate } from "../../../router/types";

const NOW = Date.parse("2026-10-08T12:00:00Z");

function d(id: string, extra: Partial<Delegate> = {}): Delegate {
  return {
    id: `dlg_${id}`,
    session_id: "s1",
    harness_id: id,
    description: `task ${id}`,
    type: "general-purpose",
    state: "running",
    runs: 1,
    started_at: "2026-10-08T11:58:00Z",
    created_at: "2026-10-08T11:58:00Z",
    updated_at: "2026-10-08T11:58:00Z",
    ...extra,
  };
}

describe("visibleDelegates", () => {
  const ago = (ms: number) => new Date(NOW - ms).toISOString();
  const fresh = d("fresh", { state: "completed", ended_at: ago(10_000) });
  const stale = d("stale", { state: "failed", ended_at: ago(DEFAULT_DELEGATE_FADE_MS + 1) });
  const ancient = d("ancient", { state: "completed", ended_at: ago(30 * 24 * 3600_000) });
  const ids = (list: Delegate[]) => list.map((x) => x.harness_id);

  it("keeps every running delegate, and finished ones for the fade window (10 min by default)", () => {
    expect(ids(visibleDelegates([d("a"), fresh, stale], NOW))).toEqual(["a", "fresh"]);
  });

  it("follows the setting: 0 min shows only running ones, Never shows every one", () => {
    expect(ids(visibleDelegates([d("a"), fresh, stale], NOW, delegateFadeMs("0m")))).toEqual(["a"]);
    expect(ids(visibleDelegates([ancient, d("a"), fresh, stale], NOW, delegateFadeMs("never")))).toEqual([
      "a",
      "fresh",
      "stale",
      "ancient",
    ]);
    expect(delegateFadeMs(undefined)).toBe(DEFAULT_DELEGATE_FADE_MS);
  });

  it("puts running ones first, then the most recently finished — what folds behind '+N more' is the oldest", () => {
    const early = d("early", { started_at: "2026-10-08T11:00:00Z" });
    expect(ids(visibleDelegates([stale, fresh, d("late"), early], NOW, Infinity))).toEqual([
      "early",
      "late",
      "fresh",
      "stale",
    ]);
  });
});

describe("DelegateRows", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(NOW);
  });
  afterEach(() => vi.useRealTimers());

  it("renders one row per delegate with a live clock while running and the ending's glyph after", () => {
    render(
      <DelegateRows
        delegates={[
          d("run"),
          d("done", { state: "failed", reason: "failed", ended_at: new Date(NOW - 5_000).toISOString(), duration_ms: 31_300 }),
        ]}
      />,
    );
    const rows = screen.getAllByRole("button");
    expect(rows[0].textContent).toContain("task run");
    expect(rows[0].textContent).toContain("2m 00s");
    expect(rows[0].querySelector('[data-status="in_progress"]')).toBeTruthy();
    expect(rows[1].textContent).toContain("31.3s");
    expect(rows[1].querySelector('[data-status="failed"]')).toBeTruthy();
  });

  it("times a resumed delegate across its runs, running and ended", () => {
    render(
      <DelegateRows
        delegates={[
          // 29m 05s of earlier runs; this one started 2m ago.
          d("resumed", { runs: 3, prior_duration_ms: 1_745_000 }),
          // Ended with no harness duration: its last run's span plus the earlier runs'.
          d("ended", { state: "stopped", runs: 2, prior_duration_ms: 60_000, ended_at: new Date(NOW - 60_000).toISOString() }),
        ]}
      />,
    );
    const rows = screen.getAllByRole("button");
    expect(rows[0].textContent).toContain("31m 05s");
    expect(rows[1].textContent).toContain("2m 00s");
  });

  it("folds past three rows behind '+N more'", () => {
    render(<DelegateRows delegates={["a", "b", "c", "e", "f"].map((id) => d(id))} />);
    expect(screen.getAllByText(/^task /)).toHaveLength(3);
    fireEvent.click(screen.getByText("+2 more"));
    expect(screen.getAllByText(/^task /)).toHaveLength(5);
    expect(screen.getByText("Show fewer")).toBeTruthy();
  });

  it("keeps the open delegate's row when it would otherwise fold behind '+N more'", () => {
    const ids = ["a", "b", "c", "e", "f"];
    render(<DelegateRows delegates={ids.map((id) => d(id))} selectedHarnessId="f" />);
    expect(screen.getAllByText(/^task /).map((el) => el.textContent)).toEqual(["task a", "task b", "task c", "task f"]);
    expect(screen.getByText("+1 more")).toBeTruthy();
  });

  it("opens the delegate it names, and marks the open one selected", () => {
    const onOpen = vi.fn();
    render(<DelegateRows delegates={[d("a"), d("b")]} selectedHarnessId="b" onOpen={onOpen} />);
    fireEvent.click(screen.getByText("task a"));
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ harness_id: "a" }));
    expect(screen.getByText("task b").closest("button")?.hasAttribute("data-selected")).toBe(true);
  });

  it("renders nothing once every delegate has aged out", () => {
    const { container } = render(
      <DelegateRows delegates={[d("old", { state: "completed", ended_at: "2026-10-08T11:00:00Z" })]} />,
    );
    expect(container.innerHTML).toBe("");
  });
});
