import { describe, expect, it, vi } from "vitest";
import { act, createEvent, fireEvent, render, screen } from "@testing-library/react";
import { SessionRail } from "./SessionRail";
import { activeOrder, doneOrder, rankBetween } from "./railOrder";
import type { Session } from "../../../router/types";

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

function rail() {
  return screen.getByLabelText("Session list");
}

function cardName(row: Element): string {
  return row.querySelector('[class*="name"]')!.textContent!;
}

/** The rail's rendered card order, top to bottom. */
function railOrder(): string[] {
  return [...rail().querySelectorAll('div[role="button"]')].map(cardName);
}

function card(name: string): HTMLElement {
  return screen.getByText(name).closest<HTMLElement>('[role="button"]')!;
}

// Started newest-first: alpha, then bravo, then charlie.
const alpha = makeSession({ id: "a", name: "alpha", created_at: "2026-07-01T10:00:00Z" });
const bravo = makeSession({ id: "b", name: "bravo", created_at: "2026-07-01T09:00:00Z" });
const charlie = makeSession({ id: "c", name: "charlie", created_at: "2026-07-01T08:00:00Z" });
const ms = (iso: string) => Date.parse(iso);

describe("activeOrder", () => {
  it("is start time, newest first, until the user moves something", () => {
    expect(activeOrder([charlie, alpha, bravo]).map((s) => s.name)).toEqual(["alpha", "bravo", "charlie"]);
  });

  it("puts a moved card wherever its rank says", () => {
    const lifted = { ...charlie, rail_rank: ms(alpha.created_at) + 1 };
    expect(activeOrder([alpha, bravo, lifted]).map((s) => s.name)).toEqual(["charlie", "alpha", "bravo"]);
  });

  it("ignores replies — a turn ending never moves a card", () => {
    const replied = { ...charlie, last_turn_at: "2026-07-02T00:00:00Z", updated_at: "2026-07-02T00:00:00Z" };
    expect(activeOrder([alpha, bravo, replied]).map((s) => s.name)).toEqual(["alpha", "bravo", "charlie"]);
  });
});

describe("doneOrder", () => {
  it("is most recently finished first, whatever the start time", () => {
    const oldButJustFinished = { ...charlie, last_turn_at: "2026-07-05T00:00:00Z" };
    const finishedEarlier = { ...alpha, last_turn_at: "2026-07-02T00:00:00Z" };
    expect(doneOrder([finishedEarlier, oldButJustFinished], (s) => s).map((s) => s.name)).toEqual([
      "charlie",
      "alpha",
    ]);
  });
});

describe("rankBetween", () => {
  it("is the midpoint between two neighbours", () => {
    expect(rankBetween(10, 20)).toBe(15);
  });

  it("goes just above the top card, or just below the bottom one", () => {
    expect(rankBetween(undefined, 20)).toBe(21);
    expect(rankBetween(10, undefined)).toBe(9);
  });
});

describe("the rail's order", () => {
  it("holds still through replies and live-state flips — nothing moves a card but the user", () => {
    const { rerender } = render(<SessionRail sessions={[alpha, bravo, charlie]} openTaskCounts={{}} />);
    expect(railOrder()).toEqual(["alpha", "bravo", "charlie"]);
    const busy = { ...charlie, last_turn_at: "2026-07-03T00:00:00Z" };
    rerender(
      <SessionRail
        sessions={[alpha, bravo, busy]}
        openTaskCounts={{}}
        agentStates={{ c: { subagents: 0, workingSince: "2026-07-03T00:00:00Z" } }}
      />,
    );
    expect(railOrder()).toEqual(["alpha", "bravo", "charlie"]);
  });

  it("drops a new session on top", () => {
    const { rerender } = render(<SessionRail sessions={[alpha, bravo]} openTaskCounts={{}} />);
    const fresh = makeSession({ id: "n", name: "fresh", created_at: "2026-07-02T00:00:00Z" });
    rerender(<SessionRail sessions={[alpha, bravo, fresh]} openTaskCounts={{}} />);
    expect(railOrder()).toEqual(["fresh", "alpha", "bravo"]);
  });
});

/** A DataTransfer stand-in: jsdom has none. */
function dataTransfer() {
  return { setData: vi.fn(), setDragImage: vi.fn(), effectAllowed: "", dropEffect: "" };
}

function grip(name: string): HTMLElement {
  return card(name).querySelector<HTMLElement>("[data-rail-grip]")!;
}

/**
 * Drag one card's grip onto another card's slot. jsdom lays nothing out (every
 * rect is zero-sized at 0) and has no DragEvent, so clientY has to be set on
 * the event by hand: negative is the slot's top half ("before"), positive its
 * bottom half ("after").
 */
function dragOnto(from: string, to: string, side: "before" | "after") {
  const dt = dataTransfer();
  fireEvent.dragStart(grip(from), { dataTransfer: dt });
  const slot = card(to).parentElement!;
  for (const make of [createEvent.dragOver, createEvent.drop]) {
    const event = make(slot, { dataTransfer: dt });
    Object.defineProperty(event, "clientY", { value: side === "before" ? -1 : 1 });
    fireEvent(slot, event);
  }
  fireEvent.dragEnd(grip(from), { dataTransfer: dt });
}

describe("reordering the active list", () => {
  it("drags a card above another: the move shows at once and persists its rank", () => {
    const onMove = vi.fn(() => new Promise<void>(() => {}));
    render(<SessionRail sessions={[alpha, bravo, charlie]} openTaskCounts={{}} onMove={onMove} />);

    dragOnto("charlie", "alpha", "before");

    expect(onMove).toHaveBeenCalledWith("c", ms(alpha.created_at) + 1);
    expect(railOrder()).toEqual(["charlie", "alpha", "bravo"]);
  });

  it("drops between two cards at the midpoint of their ranks", () => {
    const onMove = vi.fn(() => new Promise<void>(() => {}));
    render(<SessionRail sessions={[alpha, bravo, charlie]} openTaskCounts={{}} onMove={onMove} />);

    dragOnto("alpha", "bravo", "after");

    expect(onMove).toHaveBeenCalledWith("a", (ms(bravo.created_at) + ms(charlie.created_at)) / 2);
    expect(railOrder()).toEqual(["bravo", "alpha", "charlie"]);
  });

  it("puts the card back if the write fails", async () => {
    let reject!: (err: Error) => void;
    const onMove = vi.fn(() => new Promise<void>((_, r) => (reject = r)));
    render(<SessionRail sessions={[alpha, bravo, charlie]} openTaskCounts={{}} onMove={onMove} />);

    dragOnto("charlie", "alpha", "before");
    expect(railOrder()).toEqual(["charlie", "alpha", "bravo"]);
    await act(async () => reject(new Error("offline")));
    expect(railOrder()).toEqual(["alpha", "bravo", "charlie"]);
  });

  it("keeps the move when the record catches up", () => {
    const onMove = vi.fn(() => new Promise<void>(() => {}));
    const { rerender } = render(
      <SessionRail sessions={[alpha, bravo, charlie]} openTaskCounts={{}} onMove={onMove} />,
    );
    dragOnto("charlie", "alpha", "before");
    const persisted = { ...charlie, rail_rank: ms(alpha.created_at) + 1 };
    rerender(<SessionRail sessions={[alpha, bravo, persisted]} openTaskCounts={{}} onMove={onMove} />);
    expect(railOrder()).toEqual(["charlie", "alpha", "bravo"]);
  });

  it("moves the focused card one slot with ⌥↓ / ⌥↑", () => {
    const onMove = vi.fn(() => new Promise<void>(() => {}));
    render(<SessionRail sessions={[alpha, bravo, charlie]} openTaskCounts={{}} onMove={onMove} />);

    fireEvent.keyDown(card("alpha"), { key: "ArrowDown", altKey: true });
    expect(onMove).toHaveBeenLastCalledWith("a", (ms(bravo.created_at) + ms(charlie.created_at)) / 2);
    expect(railOrder()).toEqual(["bravo", "alpha", "charlie"]);

    // Off the top is a no-op, not a wrap.
    fireEvent.keyDown(card("bravo"), { key: "ArrowUp", altKey: true });
    expect(onMove).toHaveBeenCalledTimes(1);
  });

  it("offers no grip while a filter or search narrows the list, or with nowhere to persist", () => {
    const onMove = vi.fn(() => Promise.resolve());
    const { rerender } = render(<SessionRail sessions={[alpha, bravo]} openTaskCounts={{}} />);
    expect(rail().querySelector("[data-rail-grip]")).toBeNull();

    rerender(<SessionRail sessions={[alpha, bravo]} openTaskCounts={{}} onMove={onMove} />);
    expect(rail().querySelectorAll("[data-rail-grip]")).toHaveLength(2);

    fireEvent.change(screen.getByLabelText("Search sessions"), { target: { value: "alp" } });
    expect(rail().querySelector("[data-rail-grip]")).toBeNull();
    fireEvent.change(screen.getByLabelText("Search sessions"), { target: { value: "" } });

    fireEvent.click(screen.getByRole("button", { name: /^Idle/ }));
    expect(rail().querySelector("[data-rail-grip]")).toBeNull();
  });
});
