import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { SessionRail } from "./SessionRail";
import { FakeRouterClient } from "../../../router/fixtures";
import type { LandResult, Session } from "../../../router/types";

// Commit and Land on the rail (docs/mockups/session-worktrees.html §1, §5):
// the quiet work marker, and Close asking once about unlanded commits.

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
  const onClose = vi.fn();
  render(<SessionRail sessions={[session]} openTaskCounts={{}} onClose={onClose} routerClient={client} />);
  return { onClose, client };
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

describe("SessionRail — close with unlanded commits (D6)", () => {
  it("closes straight away when nothing waits to land", () => {
    const { onClose } = renderRail(isolated({ unlanded_count: 0 }));
    fireEvent.click(screen.getByRole("button", { name: "Sleep Fix the deploy script" }));
    expect(onClose).toHaveBeenCalledWith("s1");
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });

  it("asks Land-and-close or Keep for later, and Keep for later just closes", () => {
    const { onClose, client } = renderRail(isolated());
    fireEvent.click(screen.getByRole("button", { name: "Sleep Fix the deploy script" }));
    const dialog = screen.getByRole("alertdialog");
    expect(within(dialog).getByText("Sleep “Fix the deploy script”?")).toBeTruthy();
    expect(dialog.textContent).toContain("This session has 3 commits that aren't in main yet.");
    fireEvent.click(within(dialog).getByRole("button", { name: "Keep for later" }));
    expect(onClose).toHaveBeenCalledWith("s1");
    expect(client.landCalls).toEqual([]);
  });

  it("dismissing neither lands nor closes", () => {
    const { onClose, client } = renderRail(isolated());
    fireEvent.click(screen.getByRole("button", { name: "Sleep Fix the deploy script" }));
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(onClose).not.toHaveBeenCalled();
    expect(client.landCalls).toEqual([]);
  });

  it("lands, then closes", async () => {
    const { onClose, client } = renderRail(isolated());
    fireEvent.click(screen.getByRole("button", { name: "Sleep Fix the deploy script" }));
    fireEvent.click(screen.getByRole("button", { name: "Land into main and sleep" }));
    await waitFor(() => expect(onClose).toHaveBeenCalledWith("s1"));
    expect(client.landCalls).toEqual(["s1"]);
  });

  it("stays open and says why when Land doesn't land", async () => {
    const client = new FakeRouterClient();
    client.landOutcome = { outcome: "refused", reason: "primary_dirty", base: "main", files: ["a.go"] } as LandResult;
    const { onClose } = renderRail(isolated(), client);
    fireEvent.click(screen.getByRole("button", { name: "Sleep Fix the deploy script" }));
    fireEvent.click(screen.getByRole("button", { name: "Land into main and sleep" }));
    expect(await screen.findByText(/Didn't land — still open/)).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toContain("Git won't land over a.go in your checkout.");
    expect(onClose).not.toHaveBeenCalled();
  });
});
