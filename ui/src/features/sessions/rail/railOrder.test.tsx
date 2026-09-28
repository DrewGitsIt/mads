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
    ...overrides,
  };
}

function rail() {
  return screen.getByLabelText("Session list");
}

function cardName(row: Element): string {
  return row.querySelector('[class*="name"]')!.textContent!;
}

/** The rail's rendered card order, top to bottom, across both groups — a card's index. */
function railOrder(): string[] {
  return [...rail().querySelectorAll('div[role="button"]')].map(cardName);
}

/** The cards rendered under one group header, so a heal is visible and not just implied. */
function group(name: "Active" | "Recent"): string[] {
  const header = [...rail().querySelectorAll('[class*="groupHeader"]')].find((h) =>
    h.textContent!.startsWith(name),
  );
  if (!header) return [];
  return [...header.parentElement!.querySelectorAll('div[role="button"]')].map(cardName);
}

function ghost() {
  return screen.queryByRole("button", { name: /session[s]? moved/ });
}

function hasDot(name: string, state: "on" | "attn" | "off"): boolean {
  const row = screen.getByText(name).closest('[role="button"]')!;
  return Boolean(row.querySelector(`[class*="dot-${state}"]`));
}

const alpha = makeSession({ id: "a", name: "alpha", updated_at: "2026-07-01T10:00:00Z" });
const bravo = makeSession({ id: "b", name: "bravo", updated_at: "2026-07-01T09:00:00Z" });
const charlie = makeSession({ id: "c", name: "charlie", updated_at: "2026-07-01T08:00:00Z" });
const paused = makeSession({
  id: "c",
  name: "charlie",
  activity: "inactive",
  updated_at: "2026-07-01T08:00:00Z",
});

describe("settled rail order", () => {
  it("repaints a live-state flip in place and heals its group, even when the ranking is unchanged", () => {
    const { rerender } = render(
      <SessionRail sessions={[alpha, bravo, paused]} openTaskCounts={{}} />,
    );
    expect(railOrder()).toEqual(["alpha", "bravo", "charlie"]);
    expect(group("Active")).toEqual(["alpha", "bravo"]);

    // Only `activity` moves — the ranking (recency, id) cannot see it, so the
    // flat order is identical and the card must not budge. Its group still has
    // to heal: bravo under "Active" with a dead dot would be a lie.
    rerender(
      <SessionRail
        sessions={[alpha, { ...bravo, activity: "inactive" }, paused]}
        openTaskCounts={{}}
      />,
    );

    expect(railOrder()).toEqual(["alpha", "bravo", "charlie"]);
    expect(group("Active")).toEqual(["alpha"]);
    expect(group("Recent")).toEqual(["bravo", "charlie"]);
    expect(hasDot("bravo", "off")).toBe(true);
    expect(ghost()).toBeNull();
  });

  it("holds a live-state flip that would relocate the card while the rail is hovered", () => {
    const { rerender } = render(
      <SessionRail sessions={[alpha, bravo, paused]} openTaskCounts={{}} />,
    );
    fireEvent.mouseEnter(rail());

    // alpha leads the Active group, so healing it into Recent costs it a row.
    // That is a move, and it waits behind the same boundary as any other move.
    rerender(
      <SessionRail
        sessions={[{ ...alpha, activity: "inactive" }, bravo, paused]}
        openTaskCounts={{}}
      />,
    );

    expect(railOrder()).toEqual(["alpha", "bravo", "charlie"]);
    expect(group("Active")).toEqual(["alpha", "bravo"]);
    expect(hasDot("alpha", "off")).toBe(true);
    expect(ghost()?.textContent).toContain("1 session moved");

    fireEvent.mouseLeave(rail());

    expect(railOrder()).toEqual(["bravo", "alpha", "charlie"]);
    expect(group("Active")).toEqual(["bravo"]);
    expect(group("Recent")).toEqual(["alpha", "charlie"]);
    expect(ghost()).toBeNull();
  });

  it("holds a recency change while hovered, shows the ghost, and applies it on mouseleave", () => {
    const { rerender } = render(<SessionRail sessions={[alpha, bravo]} openTaskCounts={{}} />);
    fireEvent.mouseEnter(rail());

    rerender(
      <SessionRail
        sessions={[alpha, { ...bravo, updated_at: "2026-07-01T11:00:00Z" }]}
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
        sessions={[alpha, { ...bravo, updated_at: "2026-07-01T11:00:00Z" }]}
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
    const moved = { ...bravo, updated_at: "2026-07-01T11:00:00Z" };

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
        sessions={[alpha, { ...bravo, updated_at: "2026-07-01T11:00:00Z" }]}
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
        sessions={[alpha, bravo, { ...charlie, updated_at: "2026-07-01T12:00:00Z" }]}
        openTaskCounts={{}}
        selectedId="b"
      />,
    );

    expect(railOrder()).toEqual(["charlie", "bravo", "alpha"]);
    expect(ghost()).toBeNull();
  });

  it("keeps the selected card's index when another session enters the Active group", () => {
    const { rerender } = render(
      <SessionRail sessions={[alpha, bravo, paused]} openTaskCounts={{}} selectedId="b" />,
    );
    expect(railOrder()).toEqual(["alpha", "bravo", "charlie"]);
    expect(group("Active")).toEqual(["alpha", "bravo"]);

    // charlie wakes up and is now the most recent, so it joins Active at the
    // top. bravo is open: the new Active member reorders around it and bravo
    // holds index 1 instead of being pushed to 2.
    rerender(
      <SessionRail
        sessions={[alpha, bravo, { ...charlie, updated_at: "2026-07-01T12:00:00Z" }]}
        openTaskCounts={{}}
        selectedId="b"
      />,
    );

    expect(railOrder()).toEqual(["charlie", "bravo", "alpha"]);
    expect(group("Active")).toEqual(["charlie", "bravo", "alpha"]);
    expect(group("Recent")).toEqual([]);
    expect(ghost()).toBeNull();
  });

  it("holds a selected Recent card's row when the group above it grows", () => {
    const delta = makeSession({
      id: "d",
      name: "delta",
      activity: "inactive",
      updated_at: "2026-07-01T09:30:00Z",
    });
    const echo = makeSession({
      id: "e",
      name: "echo",
      activity: "inactive",
      updated_at: "2026-07-01T07:00:00Z",
    });
    // Active [alpha] · Recent [delta, charlie, echo] — charlie is open at index 2.
    const { rerender } = render(
      <SessionRail sessions={[alpha, delta, paused, echo]} openTaskCounts={{}} selectedId="c" />,
    );
    expect(railOrder()).toEqual(["alpha", "delta", "charlie", "echo"]);
    expect(group("Recent")).toEqual(["delta", "charlie", "echo"]);

    // echo wakes up and heals into Active *below* charlie, so the block above
    // charlie gains a row that the block it is in does not give back. charlie is
    // open: it gives up a rank inside Recent to stay on the row it held.
    rerender(
      <SessionRail
        sessions={[alpha, delta, paused, { ...echo, activity: "active" }]}
        openTaskCounts={{}}
        selectedId="c"
      />,
    );

    expect(railOrder()).toEqual(["alpha", "echo", "charlie", "delta"]);
    expect(group("Active")).toEqual(["alpha", "echo"]);
    expect(group("Recent")).toEqual(["charlie", "delta"]);
  });

  it("splices a new session in at its sorted position without shifting the rows above it", () => {
    const { rerender } = render(<SessionRail sessions={[alpha, paused]} openTaskCounts={{}} />);
    fireEvent.mouseEnter(rail());
    expect(railOrder()).toEqual(["alpha", "charlie"]);

    rerender(<SessionRail sessions={[alpha, bravo, paused]} openTaskCounts={{}} />);

    // Appearing is not moving: it lands in sorted position at once — inside the
    // group it belongs to — alpha does not budge, and charlie gives up exactly
    // the one row bravo occupies.
    expect(railOrder()).toEqual(["alpha", "bravo", "charlie"]);
    expect(group("Active")).toEqual(["alpha", "bravo"]);
    expect(group("Recent")).toEqual(["charlie"]);
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
