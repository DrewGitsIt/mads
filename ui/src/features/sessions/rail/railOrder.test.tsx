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

/** The rail's rendered card order, top to bottom, across both groups. */
function railOrder(): string[] {
  const rail = screen.getByLabelText("Session list");
  return [...rail.querySelectorAll('div[role="button"]')].map(
    (row) => row.querySelector('[class*="name"]')!.textContent!,
  );
}

function ghost() {
  return screen.queryByRole("button", { name: /session[s]? moved/ });
}

const alpha = makeSession({ id: "a", name: "alpha", updated_at: "2026-07-01T10:00:00Z" });
const bravo = makeSession({ id: "b", name: "bravo", updated_at: "2026-07-01T09:00:00Z" });
const charlie = makeSession({ id: "c", name: "charlie", updated_at: "2026-07-01T08:00:00Z" });

describe("settled rail order", () => {
  it("keeps a session's index when its live state changes, even with the rail at rest", () => {
    const { rerender } = render(<SessionRail sessions={[alpha, bravo, charlie]} openTaskCounts={{}} />);
    expect(railOrder()).toEqual(["alpha", "bravo", "charlie"]);

    // Nothing but `activity` moves — the status flip must paint, not sort.
    rerender(
      <SessionRail
        sessions={[alpha, { ...bravo, activity: "inactive" }, charlie]}
        openTaskCounts={{}}
      />,
    );

    expect(railOrder()).toEqual(["alpha", "bravo", "charlie"]);
    expect(ghost()).toBeNull();
    const row = screen.getByText("bravo").closest('[role="button"]')!;
    expect(row.querySelector('[class*="dot-off"]')).toBeTruthy();
  });

  it("holds a recency change while hovered, shows the ghost, and applies it on mouseleave", () => {
    const { rerender } = render(<SessionRail sessions={[alpha, bravo]} openTaskCounts={{}} />);
    const rail = screen.getByLabelText("Session list");
    fireEvent.mouseEnter(rail);

    rerender(
      <SessionRail
        sessions={[alpha, { ...bravo, updated_at: "2026-07-01T11:00:00Z" }]}
        openTaskCounts={{}}
      />,
    );
    expect(railOrder()).toEqual(["alpha", "bravo"]);
    expect(ghost()?.textContent).toContain("1 session moved");

    fireEvent.mouseLeave(rail);
    expect(railOrder()).toEqual(["bravo", "alpha"]);
    expect(ghost()).toBeNull();
  });

  it("applies the pending order when the rail loses focus", () => {
    const { rerender } = render(<SessionRail sessions={[alpha, bravo]} openTaskCounts={{}} />);
    const rail = screen.getByLabelText("Session list");
    fireEvent.focusIn(rail);

    rerender(
      <SessionRail
        sessions={[alpha, { ...bravo, updated_at: "2026-07-01T11:00:00Z" }]}
        openTaskCounts={{}}
      />,
    );
    expect(railOrder()).toEqual(["alpha", "bravo"]);
    expect(ghost()).not.toBeNull();

    fireEvent.focusOut(rail, { relatedTarget: document.body });
    expect(railOrder()).toEqual(["bravo", "alpha"]);
  });

  it("applies the pending order when the search/filter changes", () => {
    const { rerender } = render(<SessionRail sessions={[alpha, bravo]} openTaskCounts={{}} />);
    fireEvent.mouseEnter(screen.getByLabelText("Session list"));
    const moved = { ...bravo, updated_at: "2026-07-01T11:00:00Z" };

    rerender(<SessionRail sessions={[alpha, moved]} openTaskCounts={{}} />);
    expect(railOrder()).toEqual(["alpha", "bravo"]);

    rerender(<SessionRail sessions={[alpha, moved]} openTaskCounts={{}} filterKey="alph" />);
    expect(railOrder()).toEqual(["bravo", "alpha"]);
    expect(ghost()).toBeNull();
  });

  it("applies the pending order when the ghost row is clicked", () => {
    const { rerender } = render(<SessionRail sessions={[alpha, bravo]} openTaskCounts={{}} />);
    fireEvent.mouseEnter(screen.getByLabelText("Session list"));

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

  it("splices a new session in at its sorted position without shifting the rows above it", () => {
    const { rerender } = render(<SessionRail sessions={[alpha, charlie]} openTaskCounts={{}} />);
    fireEvent.mouseEnter(screen.getByLabelText("Session list"));

    rerender(<SessionRail sessions={[alpha, bravo, charlie]} openTaskCounts={{}} />);

    // Appearing is not moving: it lands in sorted position at once, alpha does
    // not budge, and charlie gives up exactly the one row bravo occupies.
    expect(railOrder()).toEqual(["alpha", "bravo", "charlie"]);
    expect(ghost()).toBeNull();
  });

  it("lets a deleted session leave immediately, even while the rail is hovered", () => {
    const { rerender } = render(
      <SessionRail sessions={[alpha, bravo, charlie]} openTaskCounts={{}} />,
    );
    fireEvent.mouseEnter(screen.getByLabelText("Session list"));

    rerender(<SessionRail sessions={[alpha, charlie]} openTaskCounts={{}} />);

    expect(railOrder()).toEqual(["alpha", "charlie"]);
    expect(ghost()).toBeNull();
  });
});
