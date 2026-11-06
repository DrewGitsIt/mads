import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import type { HeldUpdate } from "../../router/types";
import { HeldUpdates, type HeldUpdatesProps } from "./HeldUpdates";
import { requestHeldReveal } from "./heldRevealSignal";

function heldUpdate(id: number, over: Partial<HeldUpdate> = {}): HeldUpdate {
  return {
    id,
    link_id: "lnk_1",
    source_session_id: "study",
    source_name: "Pipeline study",
    kind: "digest",
    text: `update ${id}`,
    created_at: `2026-11-05T13:5${id}:00Z`,
    noop: true,
    ...over,
  };
}

const other = { source_session_id: "synth", source_name: "Task synthesis" };

function renderHeld(over: Partial<HeldUpdatesProps> = {}) {
  const props: HeldUpdatesProps = {
    sessionId: "s1",
    held: [heldUpdate(1), heldUpdate(2, other), heldUpdate(3)],
    discarded: [],
    onDiscard: vi.fn(),
    onRestore: vi.fn(),
    onDeliver: vi.fn(),
    ...over,
  };
  render(<HeldUpdates {...props} />);
  return props;
}

describe("HeldUpdates", () => {
  it("renders nothing when nothing is held or discarded", () => {
    const { container } = render(
      <HeldUpdates sessionId="s1" held={[]} discarded={[]} onDiscard={vi.fn()} onRestore={vi.fn()} onDeliver={vi.fn()} />,
    );
    expect(container.innerHTML).toBe("");
  });

  it("lists held updates grouped by session, newest first, with no divider", () => {
    renderHeld();
    expect(screen.queryByRole("separator")).toBeNull();
    expect(screen.getByText("3 updates held")).toBeTruthy();
    expect(screen.getByText(/from 2 sessions · go with your next message/)).toBeTruthy();
    const rows = [...document.querySelectorAll("[data-find]")].map((el) => el.textContent);
    // Pipeline study's newest (3) leads, then its 1, then Task synthesis's 2.
    expect(rows).toEqual(["update 3", "update 1", "update 2"]);
    expect(screen.getByText("Pipeline study")).toBeTruthy();
    expect(screen.getByText("Task synthesis")).toBeTruthy();
  });

  it("names no session count for a single source", () => {
    renderHeld({ held: [heldUpdate(1)] });
    expect(screen.getByText("1 update held")).toBeTruthy();
    expect(screen.getByText("go with your next message")).toBeTruthy();
  });

  it("shows one line collapsed and the full markdown (without the turn anchor) expanded", () => {
    renderHeld({ held: [heldUpdate(1, { text: "[turn 4] First line\n\nSecond **bold** paragraph" })] });
    const row = document.querySelector("[data-find='held:1']") as HTMLElement;
    expect(row.textContent).toBe("First line");
    fireEvent.click(screen.getByRole("button", { name: "Expand update" }));
    expect(row.textContent).toContain("Second bold paragraph");
    expect(row.textContent).not.toContain("[turn 4]");
    expect(screen.getByText("bold").tagName).toBe("STRONG");
    fireEvent.click(screen.getByRole("button", { name: "Collapse update" }));
    expect(row.textContent).toBe("First line");
  });

  it("× discards one row, Discard all every held one, Send updates only delivers", () => {
    const props = renderHeld();
    fireEvent.click(screen.getAllByRole("button", { name: "Discard update from Task synthesis" })[0]);
    expect(props.onDiscard).toHaveBeenCalledWith([2]);
    fireEvent.click(screen.getByRole("button", { name: "Discard all" }));
    expect(props.onDiscard).toHaveBeenLastCalledWith([1, 2, 3]);
    fireEvent.click(screen.getByRole("button", { name: "Send updates only" }));
    expect(props.onDeliver).toHaveBeenCalledOnce();
  });

  it("waits while a delivery is in flight", () => {
    renderHeld({ delivering: true });
    expect((screen.getByRole("button", { name: "Sending…" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "Discard all" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("offers Undo for what was discarded, also once nothing is held", () => {
    const props = renderHeld({ held: [], discarded: [heldUpdate(1), heldUpdate(2, other)] });
    const section = screen.getByRole("region", { name: "Held updates" });
    expect(within(section).getByText("2 discarded · Pipeline study, Task synthesis")).toBeTruthy();
    expect(within(section).queryByText(/held$/)).toBeNull();
    fireEvent.click(within(section).getByRole("button", { name: "Undo" }));
    expect(props.onRestore).toHaveBeenCalledWith([1, 2]);
  });

  it("brings itself into view when the rail asked for this session", () => {
    const onReveal = vi.fn();
    requestHeldReveal("s1");
    renderHeld({ onReveal });
    expect(onReveal).toHaveBeenCalledOnce();
    expect(onReveal.mock.calls[0][0]).toBe(screen.getByRole("region", { name: "Held updates" }));
    // Taken once; an ask for another session is not this list's.
    requestHeldReveal("s2");
    expect(onReveal).toHaveBeenCalledOnce();
    requestHeldReveal("s1");
    expect(onReveal).toHaveBeenCalledTimes(2);
  });
});
