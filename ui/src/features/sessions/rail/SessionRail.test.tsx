import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
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

describe("SessionRail", () => {
  it("renders an Active group with a count and a Recent group without one", () => {
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "a1", activity: "active" }),
          makeSession({ id: "r1", activity: "inactive" }),
        ]}
        openTaskCounts={{}}
      />,
    );
    expect(screen.getByText("· 1")).toBeTruthy();
    expect(screen.getByText("Recent")).toBeTruthy();
  });

  it("omits the Recent group entirely when there are no recent sessions", () => {
    render(
      <SessionRail sessions={[makeSession({ id: "a1", activity: "active" })]} openTaskCounts={{}} />,
    );
    expect(screen.queryByText("Recent")).toBeNull();
  });

  it("shows an amber attention dot for a recent session with open tasks", () => {
    render(
      <SessionRail
        sessions={[makeSession({ id: "r1", activity: "inactive", name: "needs work" })]}
        openTaskCounts={{ r1: 2 }}
      />,
    );
    const row = screen.getByText("needs work").closest('[role="button"]')!;
    expect(row.querySelector('[class*="dot-attn"]')).toBeTruthy();
    expect(screen.getByText("2 open")).toBeTruthy();
  });

  it("fires onSelect with the session id when a row is clicked", () => {
    const onSelect = vi.fn();
    render(
      <SessionRail
        sessions={[makeSession({ id: "a1", name: "pick me" })]}
        openTaskCounts={{}}
        onSelect={onSelect}
      />,
    );
    fireEvent.click(screen.getByText("pick me"));
    expect(onSelect).toHaveBeenCalledWith("a1");
  });

  it("shows a resume affordance only for non-active sessions when onResume is passed", () => {
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "a1", name: "live one", activity: "active" }),
          makeSession({ id: "r1", name: "resume me", activity: "inactive" }),
        ]}
        openTaskCounts={{}}
        onResume={vi.fn()}
      />,
    );
    expect(screen.queryByRole("button", { name: "Resume live one" })).toBeNull();
    expect(screen.getByRole("button", { name: "Resume resume me" })).toBeTruthy();
  });

  it("omits the resume affordance entirely when onResume is not passed", () => {
    render(
      <SessionRail
        sessions={[makeSession({ id: "r1", name: "resume me", activity: "inactive" })]}
        openTaskCounts={{}}
      />,
    );
    expect(screen.queryByRole("button", { name: "Resume resume me" })).toBeNull();
  });

  it("clicking resume calls onResume with the id and not onSelect", () => {
    const onSelect = vi.fn();
    const onResume = vi.fn();
    render(
      <SessionRail
        sessions={[makeSession({ id: "r1", name: "resume me", activity: "inactive" })]}
        openTaskCounts={{}}
        onSelect={onSelect}
        onResume={onResume}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Resume resume me" }));
    expect(onResume).toHaveBeenCalledWith("r1");
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("renders a ⋯ actions trigger for every row, active and inactive alike", () => {
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "a1", name: "live one", activity: "active" }),
          makeSession({ id: "r1", name: "resume me", activity: "inactive" }),
        ]}
        openTaskCounts={{}}
      />,
    );
    expect(screen.getByRole("button", { name: "live one actions" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "resume me actions" })).toBeTruthy();
  });

  it("⋯ trigger opens a menu with all three actions, disabled to match the row's activity", () => {
    render(
      <SessionRail
        sessions={[makeSession({ id: "a1", name: "live one", activity: "active" })]}
        openTaskCounts={{}}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "live one actions" }));

    // Scoped to the popover's list — the row itself is also role="button"
    // and its computed accessible name folds in all descendant text
    // (including "Resume"/"Delete"), so an unscoped regex query would match
    // both the row and the menu item.
    const menu = within(screen.getByRole("list"));
    expect((menu.getByRole("button", { name: "Close" }) as HTMLButtonElement).disabled).toBe(
      false,
    );
    expect(
      (menu.getByRole("button", { name: /Resume/ }) as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(
      (menu.getByRole("button", { name: /Delete/ }) as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  it("clicking an enabled Close/Resume in the row menu calls onClose/onResume with the row's id, not onSelect", () => {
    const onSelect = vi.fn();
    const onClose = vi.fn();
    const onResume = vi.fn();
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "a1", name: "live one", activity: "active" }),
          makeSession({ id: "r1", name: "resume me", activity: "inactive" }),
        ]}
        openTaskCounts={{}}
        onSelect={onSelect}
        onClose={onClose}
        onResume={onResume}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "live one actions" }));
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalledWith("a1");

    fireEvent.click(screen.getByRole("button", { name: "resume me actions" }));
    fireEvent.click(screen.getByRole("button", { name: "Resume" }));
    expect(onResume).toHaveBeenCalledWith("r1");

    expect(onSelect).not.toHaveBeenCalled();
  });

  it("clicking Delete in the row menu opens the shared confirm dialog; confirming targets the right row, canceling calls neither", () => {
    const onDelete = vi.fn();
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "a1", name: "leave me", activity: "inactive" }),
          makeSession({ id: "r1", name: "delete me", activity: "inactive" }),
        ]}
        openTaskCounts={{}}
        onDelete={onDelete}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "delete me actions" }));
    fireEvent.click(screen.getByRole("button", { name: "Delete" }));

    const dialog = screen.getByRole("alertdialog");
    expect(dialog.textContent).toContain("event log survive");

    fireEvent.click(screen.getByText("Cancel"));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(onDelete).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "delete me actions" }));
    fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    fireEvent.click(within(screen.getByRole("alertdialog")).getByText("Delete"));

    expect(onDelete).toHaveBeenCalledWith("r1");
  });
});
