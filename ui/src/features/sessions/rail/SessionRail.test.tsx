import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { SessionRail } from "./SessionRail";
import { FLAGS } from "../../../featureFlags";
import { FakeRouterClient } from "../../../router/fixtures";
import type { Link, Session } from "../../../router/types";

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

function makeLink(overrides: Partial<Link>): Link {
  return {
    id: "link-1",
    granting_session_id: "granter",
    subject_session_id: "subject",
    grants: ["read"],
    cadence: "per_turn",
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

describe("SessionRail — linked sessions (flag-gated)", () => {
  afterEach(() => {
    FLAGS.linkedSessions = false;
  });

  const lead = makeSession({ id: "lead", name: "investigator" });
  const a = makeSession({ id: "a", name: "payments refactor" });
  const link = makeLink({
    id: "l1",
    granting_session_id: "lead",
    subject_session_id: "a",
    grants: ["read", "inject", "orchestrate"],
  });

  it("renders zero linked-sessions DOM when the flag is off, even with links present", () => {
    FLAGS.linkedSessions = false;
    render(
      <SessionRail sessions={[lead, a]} openTaskCounts={{}} links={[link]} />,
    );
    expect(screen.queryByText("LEADER")).toBeNull();
    expect(screen.queryAllByRole("checkbox").length).toBe(0);
    // Flag off: no nesting either, so the subject still renders as its own flat row.
    expect(screen.getByText("payments refactor")).toBeTruthy();
  });

  it("shows the holder badge and nests the subject row with a LINKED badge when the flag is on", () => {
    FLAGS.linkedSessions = true;
    render(
      <SessionRail sessions={[lead, a]} openTaskCounts={{}} links={[link]} />,
    );
    expect(screen.getByText("LEADER")).toBeTruthy();
    expect(screen.getByText("LINKED")).toBeTruthy();
    expect(screen.getAllByRole("checkbox").length).toBe(2);
  });

  it("shows a Link N sessions… bar once 2+ rows are checked, and Clear resets it", () => {
    FLAGS.linkedSessions = true;
    const b = makeSession({ id: "b", name: "gateway hardening" });
    render(<SessionRail sessions={[lead, a, b]} openTaskCounts={{}} />);

    expect(screen.queryByText(/Link \d sessions…/)).toBeNull();
    fireEvent.click(screen.getByLabelText("Select investigator"));
    expect(screen.queryByText(/Link \d sessions…/)).toBeNull();
    fireEvent.click(screen.getByLabelText("Select gateway hardening"));
    expect(screen.getByText("Link 2 sessions…")).toBeTruthy();

    fireEvent.click(screen.getByText("Clear"));
    expect(screen.queryByText(/Link \d sessions…/)).toBeNull();
  });

  it("opens the chooser dialog and creates a link via the router client", async () => {
    FLAGS.linkedSessions = true;
    const b = makeSession({ id: "b", name: "gateway hardening" });
    const client = new FakeRouterClient();
    const createLinkSpy = vi.spyOn(client, "createLink");
    render(
      <SessionRail sessions={[lead, b]} openTaskCounts={{}} routerClient={client} />,
    );

    fireEvent.click(screen.getByLabelText("Select investigator"));
    fireEvent.click(screen.getByLabelText("Select gateway hardening"));
    fireEvent.click(screen.getByText("Link 2 sessions…"));

    expect(screen.getByRole("dialog")).toBeTruthy();
    fireEvent.click(screen.getByText("Create 1 link"));

    await waitFor(() => expect(createLinkSpy).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("offers Unlink only on the nested subject row, and confirming deletes the link via the router client", async () => {
    FLAGS.linkedSessions = true;
    const client = new FakeRouterClient();
    const deleteLinkSpy = vi.spyOn(client, "deleteLink");
    render(
      <SessionRail
        sessions={[lead, a]}
        openTaskCounts={{}}
        links={[link]}
        routerClient={client}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "investigator actions" }));
    expect(within(screen.getByRole("list")).queryByText("Unlink")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "investigator actions" }));

    fireEvent.click(screen.getByRole("button", { name: "payments refactor actions" }));
    fireEvent.click(within(screen.getByRole("list")).getByText("Unlink"));

    const dialog = screen.getByRole("alertdialog");
    expect(dialog.textContent).toContain("payments refactor");
    fireEvent.click(within(dialog).getByText("Unlink"));

    await waitFor(() => expect(deleteLinkSpy).toHaveBeenCalledWith("l1"));
  });
});
