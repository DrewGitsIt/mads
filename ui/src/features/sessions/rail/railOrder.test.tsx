import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { SessionRail } from "./SessionRail";
import type { Session } from "../../../router/types";

function makeSession(overrides: Partial<Session>): Session {
  return {
    id: "s1",
    name: "session",
    activity: "active",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    last_turn_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

function rail() {
  return screen.getByLabelText("Session list");
}

function cardName(row: Element): string {
  return row.querySelector('[class*="name"]')!.textContent!;
}

/** The rail's rendered card order, top to bottom — a card's index. */
function railOrder(): string[] {
  return [...rail().querySelectorAll('div[role="button"]')].map(cardName);
}

function ghost() {
  return screen.queryByRole("button", { name: /session[s]? moved/ });
}

function isResting(name: string): boolean {
  const row = screen.getByText(name).closest('[role="button"]')!;
  // The card no longer prints whether a provider child is alive — "Ready"
  // and "Sleeping" are both "Idle" now. What these tests actually pin is that
  // a live-state flip repaints in place, so they watch the tone class, which
  // still moves: an in-flight turn is status-working, a resting one is not.
  return !(row.querySelector('[role="status"]')?.className ?? "").includes("status-working");
}

const alpha = makeSession({ id: "a", name: "alpha", last_turn_at: "2026-07-01T10:00:00Z" });
const bravo = makeSession({ id: "b", name: "bravo", last_turn_at: "2026-07-01T09:00:00Z" });
const charlie = makeSession({ id: "c", name: "charlie", last_turn_at: "2026-07-01T08:00:00Z" });
const paused = makeSession({
  id: "c",
  name: "charlie",
  activity: "inactive",
  last_turn_at: "2026-07-01T08:00:00Z",
});

describe("settled rail order", () => {
  it("repaints a live-state flip in place — one list means there is nowhere for it to move to", () => {
    const { rerender } = render(
      <SessionRail sessions={[alpha, bravo, paused]} openTaskCounts={{}} />,
    );
    expect(railOrder()).toEqual(["alpha", "bravo", "charlie"]);

    // Only `activity` moves. The ranking (recency, id) cannot see it, and with
    // the Active/Recent split retired there is no group membership left for it
    // to change either — so the card repaints exactly where it is.
    rerender(
      <SessionRail
        sessions={[alpha, { ...bravo, activity: "inactive" }, paused]}
        openTaskCounts={{}}
      />,
    );

    expect(railOrder()).toEqual(["alpha", "bravo", "charlie"]);
    expect(isResting("bravo")).toBe(true);
    expect(ghost()).toBeNull();
  });

  it("does not move the top card — or raise a ghost — when it goes to sleep, even mid-hover", () => {
    const { rerender } = render(
      <SessionRail sessions={[alpha, bravo, paused]} openTaskCounts={{}} />,
    );
    fireEvent.mouseEnter(rail());

    // Under Active/Recent this cost alpha two rows. Now it costs it nothing:
    // there is no pending move to hold, so no ghost, and mouseleave is a no-op.
    rerender(
      <SessionRail
        sessions={[{ ...alpha, activity: "inactive" }, bravo, paused]}
        openTaskCounts={{}}
      />,
    );

    expect(railOrder()).toEqual(["alpha", "bravo", "charlie"]);
    expect(isResting("alpha")).toBe(true);
    expect(ghost()).toBeNull();

    fireEvent.mouseLeave(rail());
    expect(railOrder()).toEqual(["alpha", "bravo", "charlie"]);
  });

  it("does not move a card that was merely renamed — the order reads last_turn_at", () => {
    const { rerender } = render(
      <SessionRail sessions={[alpha, bravo, charlie]} openTaskCounts={{}} />,
    );
    expect(railOrder()).toEqual(["alpha", "bravo", "charlie"]);

    // The auto-title job lands on the oldest card: a new name and a fresh
    // updated_at, but no reply. Under updated_at this shot it to the top (and
    // raised a ghost while hovered, for a move the user never caused).
    rerender(
      <SessionRail
        sessions={[alpha, bravo, { ...charlie, name: "renamed", updated_at: "2026-07-01T11:00:00Z" }]}
        openTaskCounts={{}}
      />,
    );

    expect(railOrder()).toEqual(["alpha", "bravo", "renamed"]);
    expect(ghost()).toBeNull();
  });

  it("holds a recency change while hovered, shows the ghost, and applies it on mouseleave", () => {
    const { rerender } = render(<SessionRail sessions={[alpha, bravo]} openTaskCounts={{}} />);
    fireEvent.mouseEnter(rail());

    rerender(
      <SessionRail
        sessions={[alpha, { ...bravo, last_turn_at: "2026-07-01T11:00:00Z" }]}
        openTaskCounts={{}}
      />,
    );
    expect(railOrder()).toEqual(["alpha", "bravo"]);
    expect(ghost()?.textContent).toContain("1 session moved");

    fireEvent.mouseLeave(rail());
    expect(railOrder()).toEqual(["bravo", "alpha"]);
    expect(ghost()).toBeNull();
  });

  it("applies the pending order when the rail loses focus", () => {
    const { rerender } = render(<SessionRail sessions={[alpha, bravo]} openTaskCounts={{}} />);
    fireEvent.focusIn(rail());

    rerender(
      <SessionRail
        sessions={[alpha, { ...bravo, last_turn_at: "2026-07-01T11:00:00Z" }]}
        openTaskCounts={{}}
      />,
    );
    expect(railOrder()).toEqual(["alpha", "bravo"]);
    expect(ghost()).not.toBeNull();

    fireEvent.focusOut(rail(), { relatedTarget: document.body });
    expect(railOrder()).toEqual(["bravo", "alpha"]);
  });

  it("applies the pending order when the search/filter changes", () => {
    const { rerender } = render(<SessionRail sessions={[alpha, bravo]} openTaskCounts={{}} />);
    fireEvent.mouseEnter(rail());
    const moved = { ...bravo, last_turn_at: "2026-07-01T11:00:00Z" };

    rerender(<SessionRail sessions={[alpha, moved]} openTaskCounts={{}} />);
    expect(railOrder()).toEqual(["alpha", "bravo"]);

    rerender(<SessionRail sessions={[alpha, moved]} openTaskCounts={{}} filterKey="alph" />);
    expect(railOrder()).toEqual(["bravo", "alpha"]);
    expect(ghost()).toBeNull();
  });

  it("applies the pending order when the ghost row is clicked", () => {
    const { rerender } = render(<SessionRail sessions={[alpha, bravo]} openTaskCounts={{}} />);
    fireEvent.mouseEnter(rail());

    rerender(
      <SessionRail
        sessions={[alpha, { ...bravo, last_turn_at: "2026-07-01T11:00:00Z" }]}
        openTaskCounts={{}}
      />,
    );
    fireEvent.click(ghost()!);

    expect(railOrder()).toEqual(["bravo", "alpha"]);
    expect(ghost()).toBeNull();
  });

  it("never moves the selected card, even across a settle", () => {
    const { rerender } = render(
      <SessionRail sessions={[alpha, bravo, charlie]} openTaskCounts={{}} selectedId="b" />,
    );
    expect(railOrder()).toEqual(["alpha", "bravo", "charlie"]);

    // charlie becomes the most recent: it takes the top, but bravo — the open
    // session — keeps index 1 rather than being pushed down by it.
    rerender(
      <SessionRail
        sessions={[alpha, bravo, { ...charlie, last_turn_at: "2026-07-01T12:00:00Z" }]}
        openTaskCounts={{}}
        selectedId="b"
      />,
    );

    expect(railOrder()).toEqual(["charlie", "bravo", "alpha"]);
    expect(ghost()).toBeNull();
  });

  it("keeps the selected card's index when a session below it becomes the most recent", () => {
    const { rerender } = render(
      <SessionRail sessions={[alpha, bravo, paused]} openTaskCounts={{}} selectedId="b" />,
    );
    expect(railOrder()).toEqual(["alpha", "bravo", "charlie"]);

    // charlie wakes and takes the top. bravo is open: everything reorders
    // around it and it holds index 1 instead of being pushed to 2.
    rerender(
      <SessionRail
        sessions={[alpha, bravo, { ...charlie, last_turn_at: "2026-07-01T12:00:00Z" }]}
        openTaskCounts={{}}
        selectedId="b"
      />,
    );

    expect(railOrder()).toEqual(["charlie", "bravo", "alpha"]);
    expect(ghost()).toBeNull();
  });

  it("holds the selected card's row even when the sessions above it are the ones that churn", () => {
    const delta = makeSession({
      id: "d",
      name: "delta",
      activity: "inactive",
      last_turn_at: "2026-07-01T09:30:00Z",
    });
    const echo = makeSession({
      id: "e",
      name: "echo",
      activity: "inactive",
      last_turn_at: "2026-07-01T07:00:00Z",
    });
    // alpha, delta, charlie, echo — charlie is open at index 2.
    const { rerender } = render(
      <SessionRail sessions={[alpha, delta, paused, echo]} openTaskCounts={{}} selectedId="c" />,
    );
    expect(railOrder()).toEqual(["alpha", "delta", "charlie", "echo"]);

    // echo replies and outranks everything but alpha, so by pure recency it
    // would take index 1 and push charlie to 3. charlie is open: it keeps the
    // row it held and echo settles directly above it instead.
    rerender(
      <SessionRail
        sessions={[alpha, delta, paused, { ...echo, last_turn_at: "2026-07-01T09:45:00Z" }]}
        openTaskCounts={{}}
        selectedId="c"
      />,
    );

    expect(railOrder()).toEqual(["alpha", "echo", "charlie", "delta"]);
  });

  it("splices a new session in at its sorted position without shifting the rows above it", () => {
    const { rerender } = render(<SessionRail sessions={[alpha, paused]} openTaskCounts={{}} />);
    fireEvent.mouseEnter(rail());
    expect(railOrder()).toEqual(["alpha", "charlie"]);

    rerender(<SessionRail sessions={[alpha, bravo, paused]} openTaskCounts={{}} />);

    // Appearing is not moving: it lands in sorted position at once — alpha does
    // not budge, and charlie gives up exactly the one row bravo occupies.
    expect(railOrder()).toEqual(["alpha", "bravo", "charlie"]);
    expect(ghost()).toBeNull();
  });

  it("lets a deleted session leave immediately, even while the rail is hovered", () => {
    const { rerender } = render(
      <SessionRail sessions={[alpha, bravo, paused]} openTaskCounts={{}} />,
    );
    fireEvent.mouseEnter(rail());

    rerender(<SessionRail sessions={[alpha, paused]} openTaskCounts={{}} />);

    expect(railOrder()).toEqual(["alpha", "charlie"]);
    expect(ghost()).toBeNull();
  });
});
