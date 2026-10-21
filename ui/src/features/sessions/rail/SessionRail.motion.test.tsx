import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { FOLD_MS } from "../../../components/Motion";
import type { Session } from "../../../router/types";
import { SessionRail } from "./SessionRail";
import { STRIKE_MS, useDoneMotion, type DoneMotion } from "./useDoneMotion";

// The ✓'s motion through the real rail: the card is marked at once, stays in
// Active until the server's answer and the strike's beat, folds across with
// a closing copy in Active and an opening one in Done, and only the closing
// copy carries the strike.

function makeSession(overrides: Partial<Session>): Session {
  return {
    id: "s1",
    name: "session",
    activity: "inactive",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    last_turn_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

const target = makeSession({ id: "t", name: "Settings UX audit", created_at: "2026-07-03T00:00:00Z" });
const other = makeSession({ id: "o", name: "LoRA model specialization", created_at: "2026-07-02T00:00:00Z" });
const old = makeSession({ id: "d", name: "Repo inventory review", tags: ["aside"] });

let motion: DoneMotion;
function Harness({ sessions }: { sessions: Session[] }) {
  motion = useDoneMotion();
  return (
    <SessionRail
      sessions={sessions}
      openTaskCounts={{}}
      doneMotion={motion}
      onSetDone={(session, done) => motion.sent(session, done)}
    />
  );
}

/** Cards in one list, with whether each is the closing copy of a fold. */
function list(name: "active" | "done") {
  const el = document.querySelector(`[data-rail-list="${name}"]`);
  return [...(el?.querySelectorAll<HTMLElement>("[data-rail-card]") ?? [])].map((card) => ({
    name: card.querySelector('[class*="name"]')!.textContent,
    mark: card.dataset.doneMark,
    closing: card.closest('[aria-hidden="true"]') !== null,
  }));
}

describe("SessionRail — ✓ motion", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    localStorage.clear();
  });
  afterEach(() => vi.useRealTimers());

  it("strikes, holds for the answer, folds across, and lands plain in Done", () => {
    render(<Harness sessions={[target, other, old]} />);
    fireEvent.click(screen.getByRole("button", { name: "Check off Settings UX audit" }));

    expect(list("active")[0]).toEqual({ name: "Settings UX audit", mark: "strike", closing: false });
    // The ✓ already reads as checked.
    expect(screen.getByRole("button", { name: "Bring back Settings UX audit" }).getAttribute("aria-pressed")).toBe(
      "true",
    );

    act(() => motion.confirmed({ ...target, tags: ["aside"], updated_at: "2026-07-04T00:00:00Z" }, true));
    act(() => vi.advanceTimersByTime(STRIKE_MS));
    expect(list("active")).toContainEqual({ name: "Settings UX audit", mark: "strike", closing: true });
    expect(list("done")).toContainEqual({ name: "Settings UX audit", mark: undefined, closing: false });

    act(() => vi.advanceTimersByTime(FOLD_MS));
    expect(list("active").map((c) => c.name)).toEqual(["LoRA model specialization"]);
    expect(list("done").map((c) => c.name)).toContain("Settings UX audit");
  });

  it("un-strikes and stays in Active when the server refuses", () => {
    render(<Harness sessions={[target, other, old]} />);
    fireEvent.click(screen.getByRole("button", { name: "Check off Settings UX audit" }));
    act(() => motion.failed("t"));

    expect(list("active")[0]).toEqual({ name: "Settings UX audit", mark: undefined, closing: false });
    act(() => vi.advanceTimersByTime(STRIKE_MS + FOLD_MS));
    expect(list("done").map((c) => c.name)).toEqual(["Repo inventory review"]);
  });
});
