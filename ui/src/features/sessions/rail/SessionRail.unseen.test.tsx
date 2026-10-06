import { beforeEach, describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { SessionRail } from "./SessionRail";
import { SEEN_KEY } from "./railUnseen";
import type { Session } from "../../../router/types";

function makeSession(id: string, overrides: Partial<Session> = {}): Session {
  return {
    id,
    name: `session ${id}`,
    activity: "inactive",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    last_turn_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

function card(id: string): HTMLElement {
  return screen
    .getByText(`session ${id}`)
    .closest<HTMLElement>("[data-rail-card]")!;
}

function isUnseen(id: string): boolean {
  return card(id).hasAttribute("data-unseen");
}

function stored(): string[] | null {
  const raw = localStorage.getItem(SEEN_KEY);
  return raw === null ? null : (JSON.parse(raw) as string[]).sort();
}

function rail(
  sessions: Session[],
  extra: { selectedId?: string; sessionsLoaded?: boolean } = {},
) {
  return <SessionRail sessions={sessions} openTaskCounts={{}} {...extra} />;
}

describe("SessionRail — unseen highlight", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("seeds every session already loaded as seen on first run", () => {
    render(rail([makeSession("a"), makeSession("b")]));
    expect(isUnseen("a")).toBe(false);
    expect(isUnseen("b")).toBe(false);
    expect(document.querySelector("[data-unseen]")).toBeNull();
    expect(stored()).toEqual(["a", "b"]);
  });

  it("never seeds imported sessions, so a first-run import lights up", () => {
    // A new user's rail first mounts on the sessions the wizard's import just
    // made; an upgrading user's earlier imports light up the same way.
    render(
      rail([
        makeSession("mine"),
        makeSession("imp1", { tags: ["imported"] }),
        makeSession("imp2", { tags: ["imported", "rerooted"] }),
      ]),
    );
    expect(isUnseen("mine")).toBe(false);
    expect(isUnseen("imp1")).toBe(true);
    expect(isUnseen("imp2")).toBe(true);
    expect(stored()).toEqual(["mine"]);
    fireEvent.mouseEnter(card("imp1"));
    expect(isUnseen("imp1")).toBe(false);
  });

  it("does not seed from the empty list before the first load completes", () => {
    const { rerender } = render(rail([], { sessionsLoaded: false }));
    expect(stored()).toBeNull();
    rerender(rail([makeSession("a")], { sessionsLoaded: true }));
    expect(isUnseen("a")).toBe(false);
    expect(stored()).toEqual(["a"]);
  });

  it("seeds an empty loaded list, so the very first session is new", () => {
    const { rerender } = render(rail([], { sessionsLoaded: true }));
    expect(stored()).toEqual([]);
    rerender(rail([makeSession("a")], { sessionsLoaded: true }));
    expect(isUnseen("a")).toBe(true);
  });

  it("marks a session that arrives later as unseen, with a screen-reader cue", () => {
    const { rerender } = render(rail([makeSession("a")]));
    rerender(rail([makeSession("a"), makeSession("b"), makeSession("c")]));
    expect(isUnseen("a")).toBe(false);
    expect(isUnseen("b")).toBe(true);
    expect(isUnseen("c")).toBe(true);
    expect(card("b").className).toMatch(/unseen/);
    expect(card("b").textContent).toContain("New session");
    expect(card("a").textContent).not.toContain("New session");
  });

  it("clears on hover and keeps the others lit", () => {
    const { rerender } = render(rail([makeSession("a")]));
    rerender(rail([makeSession("a"), makeSession("b"), makeSession("c")]));
    fireEvent.mouseEnter(card("b"));
    expect(isUnseen("b")).toBe(false);
    expect(card("b").textContent).not.toContain("New session");
    expect(isUnseen("c")).toBe(true);
    expect(stored()).toEqual(["a", "b"]);
  });

  it("clears on keyboard focus", () => {
    const { rerender } = render(rail([makeSession("a")]));
    rerender(rail([makeSession("a"), makeSession("b")]));
    fireEvent.focus(card("b"));
    expect(isUnseen("b")).toBe(false);
  });

  it("clears when the session is opened, and never lights a card that arrives selected", () => {
    const { rerender } = render(rail([makeSession("a")]));
    rerender(rail([makeSession("a"), makeSession("b")]));
    expect(isUnseen("b")).toBe(true);
    rerender(rail([makeSession("a"), makeSession("b")], { selectedId: "b" }));
    expect(isUnseen("b")).toBe(false);
    // Opened straight from the Launchpad: selected in the same render it appears.
    rerender(
      rail([makeSession("a"), makeSession("b"), makeSession("d")], {
        selectedId: "d",
      }),
    );
    expect(isUnseen("d")).toBe(false);
    expect(stored()).toEqual(["a", "b", "d"]);
    // Deselecting does not bring it back.
    rerender(rail([makeSession("a"), makeSession("b"), makeSession("d")]));
    expect(isUnseen("d")).toBe(false);
  });

  it("survives a remount", () => {
    const first = render(rail([makeSession("a")]));
    first.rerender(
      rail([makeSession("a"), makeSession("b"), makeSession("c")]),
    );
    fireEvent.mouseEnter(card("b"));
    first.unmount();

    render(rail([makeSession("a"), makeSession("b"), makeSession("c")]));
    expect(isUnseen("a")).toBe(false);
    expect(isUnseen("b")).toBe(false);
    expect(isUnseen("c")).toBe(true);
  });

  it("prunes ids of deleted sessions", () => {
    const { rerender } = render(
      rail([makeSession("a"), makeSession("b"), makeSession("c")]),
    );
    expect(stored()).toEqual(["a", "b", "c"]);
    rerender(rail([makeSession("a"), makeSession("c")]));
    expect(stored()).toEqual(["a", "c"]);
  });

  it("does not prune against an empty list (a failed refresh must not relight the rail)", () => {
    const { rerender } = render(rail([makeSession("a"), makeSession("b")]));
    rerender(rail([]));
    expect(stored()).toEqual(["a", "b"]);
    rerender(rail([makeSession("a"), makeSession("b")]));
    expect(document.querySelector("[data-unseen]")).toBeNull();
  });

  it("treats corrupt stored state as first run", () => {
    localStorage.setItem(SEEN_KEY, "{not json");
    render(rail([makeSession("a")]));
    expect(isUnseen("a")).toBe(false);
    expect(stored()).toEqual(["a"]);
  });

  it("is static and leans on global.css for reduced motion", () => {
    const css = readFileSync(
      "src/features/sessions/rail/SessionRail.module.css",
      "utf8",
    );
    const unseen = css.match(/\.row\.unseen \{([\s\S]*?)\n\}/)![1];
    // One-shot arrival only: nothing iterates, so there is no perpetual pulse.
    expect(unseen).not.toMatch(/infinite/);
    const global = readFileSync("src/styles/global.css", "utf8");
    const reduced = global.match(
      /@media \(prefers-reduced-motion: reduce\) \{([\s\S]*?)\n\}/,
    )![1];
    expect(reduced).toMatch(/animation: none !important/);
    expect(reduced).toMatch(/transition: none !important/);
  });
});
