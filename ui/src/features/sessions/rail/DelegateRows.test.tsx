import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { DEFAULT_DELEGATE_FADE_MS, DelegateRows, delegateFadeMs, delegateTree, visibleDelegates } from "./DelegateRows";
import type { DelegateNode } from "./DelegateRows";
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

  it("reads a waiting delegate as live: never faded, sorted with the running ones", () => {
    const waiting = d("waiting", {
      state: "completed",
      ended_at: ago(DEFAULT_DELEGATE_FADE_MS * 3),
      started_at: "2026-10-08T11:00:00Z",
      waiting_on: { subagents: 1 },
    });
    expect(ids(visibleDelegates([fresh, d("a"), waiting], NOW, 0))).toEqual(["waiting", "a"]);
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

describe("delegateTree", () => {
  const ago = (ms: number) => new Date(NOW - ms).toISOString();
  const done = (id: string, extra: Partial<Delegate> = {}) => d(id, { state: "completed", ended_at: ago(5_000), ...extra });
  const stale = (id: string, extra: Partial<Delegate> = {}) =>
    d(id, { state: "completed", ended_at: ago(DEFAULT_DELEGATE_FADE_MS + 60_000), ...extra });
  /** The tree as nested ids; a fold line reads "N finished". */
  const shape = (nodes: DelegateNode[]): unknown[] =>
    nodes.map((n) => {
      const under = [...(n.folded.length ? [`${n.folded.length} finished`] : []), ...shape(n.children)];
      return under.length ? [n.d.harness_id, under] : n.d.harness_id;
    });

  it("nests a subagent under the one that spawned it, and a row with an unknown parent at the top", () => {
    const list = [d("m"), d("kid", { parent_harness_id: "m" }), d("orphan", { parent_harness_id: "gone" })];
    expect(shape(delegateTree(list, NOW).roots)).toEqual([["m", ["kid"]], "orphan"]);
  });

  it("folds a shown parent's finished subagents into one line, first, above its live ones", () => {
    const list = [
      d("m"),
      d("live", { parent_harness_id: "m" }),
      done("f1", { parent_harness_id: "m" }),
      done("f2", { parent_harness_id: "m" }),
    ];
    expect(shape(delegateTree(list, NOW).roots)).toEqual([["m", ["2 finished", "live"]]]);
    // Unfolded, they are rows of their own, after the live ones.
    expect(shape(delegateTree(list, NOW, { unfolded: new Set(["m"]) }).roots)).toEqual([["m", ["live", "f1", "f2"]]]);
  });

  it("lists the open delegate's finished subagents in full; theirs only while live", () => {
    const list = [
      d("m"),
      done("kid", { parent_harness_id: "m" }),
      done("grandkid", { parent_harness_id: "kid" }),
      d("live-grandkid", { parent_harness_id: "kid" }),
    ];
    expect(shape(delegateTree(list, NOW, { selectedHarnessId: "m" }).roots)).toEqual([
      ["m", [["kid", ["1 finished", "live-grandkid"]]]],
    ]);
  });

  it("shows the open delegate however long ago it finished, with its parents", () => {
    const list = [stale("m"), stale("kid", { parent_harness_id: "m" })];
    expect(shape(delegateTree(list, NOW).roots)).toEqual([]);
    expect(shape(delegateTree(list, NOW, { selectedHarnessId: "kid" }).roots)).toEqual([["m", ["kid"]]]);
  });

  it("keeps a faded parent's row while a subagent of its is live, so the child has a home", () => {
    const list = [stale("m"), d("kid", { parent_harness_id: "m" }), done("sib", { parent_harness_id: "m" })];
    expect(shape(delegateTree(list, NOW).roots)).toEqual([["m", ["1 finished", "kid"]]]);
  });

  it("drops a faded parent and everything folded under it when nothing under it is live", () => {
    const list = [stale("m"), done("kid", { parent_harness_id: "m" })];
    expect(delegateTree(list, NOW).total).toBe(0);
  });

  it("counts top-level rows toward '+N more'; a row's subagents come with it", () => {
    const list = [
      d("a", { started_at: "2026-10-08T11:00:00Z" }),
      ...["k1", "k2", "k3"].map((k) => d(k, { parent_harness_id: "a" })),
      ...["b", "c", "e"].map((id) => d(id)),
    ];
    const tree = delegateTree(list, NOW);
    expect(shape(tree.roots)).toEqual([["a", ["k1", "k2", "k3"]], "b", "c"]);
    expect(tree.hidden).toBe(1);
    // The open one's branch is kept, by its top-level row.
    const opened = delegateTree([...list, d("deep", { parent_harness_id: "e" })], NOW, { selectedHarnessId: "deep" });
    expect(shape(opened.roots)).toEqual([["a", ["k1", "k2", "k3"]], "b", "c", ["e", ["deep"]]]);
    expect(opened.hidden).toBe(0);
  });

  it("treats a parent chain that loops as top-level rather than losing the rows", () => {
    const list = [d("x", { parent_harness_id: "y" }), d("y", { parent_harness_id: "x" })];
    expect(delegateTree(list, NOW).total).toBe(2);
  });
});

describe("DelegateRows nesting", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(NOW);
  });
  afterEach(() => vi.useRealTimers());

  it("draws a waiting row with the waiting dial, no number, and what it waits on in the tooltip", () => {
    render(
      <DelegateRows
        delegates={[
          d("m", {
            description: "Research mitigations",
            state: "completed",
            ended_at: new Date(NOW - 60_000).toISOString(),
            duration_ms: 120_000,
            waiting_on: { subagents: 2, processes: 1 },
          }),
          d("k1", { parent_harness_id: "m" }),
          d("k2", { parent_harness_id: "m" }),
        ]}
      />,
    );
    const row = screen.getByText("Research mitigations").closest("button")!;
    expect(row.getAttribute("data-state")).toBe("waiting");
    expect(row.querySelector('[data-status="waiting"]')).toBeTruthy();
    expect(row.textContent).toContain("waiting");
    expect(row.textContent).not.toContain("2m 00s");
    expect(row.title).toBe(
      "Research mitigations (general-purpose) — turn ended, waiting on 2 subagents and a background command",
    );
  });

  it("names one subagent and N background commands in a waiting tooltip", () => {
    render(
      <DelegateRows
        delegates={[d("m", { description: "M", state: "completed", ended_at: new Date(NOW).toISOString(), waiting_on: { subagents: 1, processes: 3 } })]}
      />,
    );
    expect(screen.getByText("M").closest("button")!.title).toBe(
      "M (general-purpose) — turn ended, waiting on 1 subagent and 3 background commands",
    );
  });

  it("nests a subagent's rows one level in, and unfolds its finished ones on a click", () => {
    const onOpen = vi.fn();
    const ended = new Date(NOW - 5_000).toISOString();
    render(
      <DelegateRows
        delegates={[
          d("m"),
          d("live", { parent_harness_id: "m" }),
          d("f1", { parent_harness_id: "m", state: "completed", ended_at: ended }),
        ]}
        onOpen={onOpen}
      />,
    );
    const nested = screen.getByTestId("delegate-nested");
    expect(nested.textContent).toContain("task live");
    expect(screen.queryByText("task f1")).toBeNull();
    const fold = screen.getByText(/^1 subagent finished/);
    // First under the parent, above its live subagent.
    expect(nested.firstElementChild).toBe(fold);
    fireEvent.click(fold);
    expect(screen.getByText("task f1")).toBeTruthy();
    expect(screen.queryByText(/subagent finished/)).toBeNull();
    expect(onOpen).not.toHaveBeenCalled();
    fireEvent.click(screen.getByText("task live"));
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ harness_id: "live" }));
  });

  it("counts only top-level rows toward '+N more'", () => {
    render(
      <DelegateRows
        delegates={[
          d("a", { started_at: "2026-10-08T11:00:00Z" }),
          d("k1", { parent_harness_id: "a" }),
          d("k2", { parent_harness_id: "a" }),
          d("b"),
          d("c"),
          d("e"),
        ]}
      />,
    );
    expect(screen.getAllByText(/^task /).map((el) => el.textContent)).toEqual([
      "task a",
      "task k1",
      "task k2",
      "task b",
      "task c",
    ]);
    fireEvent.click(screen.getByText("+1 more"));
    expect(screen.getByText("task e")).toBeTruthy();
  });
});
