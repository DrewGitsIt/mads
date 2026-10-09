import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { SessionRail } from "./SessionRail";
import { FakeRouterClient } from "../../../router/fixtures";
import type { Session } from "../../../router/types";

// The quiet work marker on the rail (docs/mockups/session-worktrees.html §1).
// Sleep's "land or keep for later" question (§5, D6) is no longer the rail's
// to ask — it moved into useSessionSleep so the workspace kebab asks it too,
// and its tests moved with it to ../actions/useSessionSleep.test.tsx.

function isolated(overrides: Partial<Session> = {}): Session {
  return {
    id: "s1",
    name: "Fix the deploy script",
    activity: "inactive",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    last_turn_at: "2026-07-01T00:00:00Z",
    worktree_dir: "/home/u/.mads/session-worktrees/s1",
    worktree_branch: "mads/session/s1",
    worktree_base: "main",
    uncommitted_count: 0,
    unlanded_count: 3,
    ...overrides,
  };
}

function renderRail(session: Session, client = new FakeRouterClient()) {
  const onSleep = vi.fn();
  render(<SessionRail sessions={[session]} openTaskCounts={{}} onSleep={onSleep} routerClient={client} />);
  return { onSleep, client };
}

describe("SessionRail — work marker", () => {
  it("is a filled dot with 'N commits to land' when commits wait to land", () => {
    renderRail(isolated());
    const mark = document.querySelector('[data-work-mark="land"]')!;
    expect(mark.getAttribute("aria-label")).toBe("3 commits to land");
    expect(mark.className).not.toMatch(/Hollow/);
    expect(screen.getByRole("status").textContent).toContain("· 3 commits to land");
  });

  it("is a hollow dot with 'uncommitted changes' while files are uncommitted", () => {
    renderRail(isolated({ uncommitted_count: 2 }));
    const mark = document.querySelector('[data-work-mark="commit"]')!;
    expect(mark.className).toMatch(/Hollow/);
    expect(screen.getByRole("status").textContent).toContain("· uncommitted changes");
  });

  it("is absent when there's no work waiting", () => {
    renderRail(isolated({ unlanded_count: 0 }));
    expect(document.querySelector("[data-work-mark]")).toBeNull();
  });

  it("keeps the status line for a live state, marking the card only", () => {
    render(
      <SessionRail
        sessions={[isolated({ activity: "active" })]}
        openTaskCounts={{}}
        agentStates={{ s1: { subagents: 0, workingSince: new Date().toISOString(), label: "Running bash" } }}
      />,
    );
    expect(document.querySelector('[data-work-mark="land"]')).toBeTruthy();
    expect(screen.getByRole("status").textContent).not.toContain("to land");
  });
});
