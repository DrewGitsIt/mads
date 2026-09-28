import { afterEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { SessionRail } from "./SessionRail";
import { FLAGS } from "../../../featureFlags";
import { FakeRouterClient } from "../../../router/fixtures";
import { FakeEventSource } from "../../../router/testFakeEventSource";
import { useSessionAgentState } from "../state/useSessionAgentState";
import type { SessionAgentState } from "./railGroups";
import type { Link, Session } from "../../../router/types";
import { readFileSync } from "node:fs";

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

  it("shows an amber attention dot for a recent session with blocked work", () => {
    render(
      <SessionRail
        sessions={[makeSession({ id: "r1", activity: "inactive", name: "needs work" })]}
        openTaskCounts={{ r1: { running: 0, ready: 0, blocked: 1 } }}
      />,
    );
    const row = screen.getByText("needs work").closest('[role="button"]')!;
    expect(row.querySelector('[class*="dot-attn"]')).toBeTruthy();
    expect(screen.getByText("1 blocked")).toBeTruthy();
  });

  it("does not show an attention dot for a recent session that only has running/ready work", () => {
    render(
      <SessionRail
        sessions={[makeSession({ id: "r1", activity: "inactive", name: "steady work" })]}
        openTaskCounts={{ r1: { running: 1, ready: 1, blocked: 0 } }}
      />,
    );
    const row = screen.getByText("steady work").closest('[role="button"]')!;
    expect(row.querySelector('[class*="dot-off"]')).toBeTruthy();
    expect(row.querySelector('[class*="dot-attn"]')).toBeNull();
  });

  it("renders only non-zero buckets, in running / ready / blocked order", () => {
    render(
      <SessionRail
        sessions={[makeSession({ id: "r1", activity: "inactive", name: "mixed work" })]}
        openTaskCounts={{ r1: { running: 2, ready: 2, blocked: 1 } }}
      />,
    );
    const row = screen.getByText("mixed work").closest('[role="button"]') as HTMLElement;
    const buckets = within(row).getAllByText(/running|ready|blocked/);
    expect(buckets.map((b) => b.textContent)).toEqual(["2 running", "2 ready", "1 blocked"]);
  });

  it("renders a single bucket alone when only one status is present", () => {
    render(
      <SessionRail
        sessions={[makeSession({ id: "r1", activity: "inactive", name: "only ready" })]}
        openTaskCounts={{ r1: { running: 0, ready: 3, blocked: 0 } }}
      />,
    );
    expect(screen.getByText("3 ready")).toBeTruthy();
    expect(screen.queryByText(/running|blocked/)).toBeNull();
  });

  it("shows no work-breakdown line at all when every bucket is zero", () => {
    render(
      <SessionRail
        sessions={[makeSession({ id: "r1", activity: "inactive", name: "idle" })]}
        openTaskCounts={{ r1: { running: 0, ready: 0, blocked: 0 } }}
      />,
    );
    expect(screen.queryByText(/running|ready|blocked/)).toBeNull();
    expect(screen.queryByText(/open$/)).toBeNull();
  });

  it("updates a card's bucket line in place on a status change, without remounting the row", () => {
    const session = makeSession({ id: "r1", activity: "inactive", name: "needs work" });
    const { rerender } = render(
      <SessionRail sessions={[session]} openTaskCounts={{ r1: { running: 0, ready: 1, blocked: 0 } }} />,
    );
    const rowBefore = screen.getByText("needs work").closest('[role="button"]');
    expect(screen.getByText("1 ready")).toBeTruthy();

    // Simulates the engine claiming the task: a task.status_changed event
    // flips it ready -> in_progress upstream in useOpenTaskCounts, which
    // re-renders SessionRail with new breakdown props.
    rerender(
      <SessionRail sessions={[session]} openTaskCounts={{ r1: { running: 1, ready: 0, blocked: 0 } }} />,
    );

    const rowAfter = screen.getByText("needs work").closest('[role="button"]');
    expect(rowAfter).toBe(rowBefore);
    expect(screen.getByText("1 running")).toBeTruthy();
    expect(screen.queryByText("1 ready")).toBeNull();
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

  it("shows no checkbox gutter or persistent kebab on any row", () => {
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "a1", name: "live one", activity: "active" }),
          makeSession({ id: "r1", name: "resume me", activity: "inactive" }),
        ]}
        openTaskCounts={{}}
      />,
    );
    expect(screen.queryAllByRole("checkbox").length).toBe(0);
    expect(screen.queryByRole("button", { name: "live one actions" })).toBeNull();
    expect(screen.queryByRole("button", { name: "resume me actions" })).toBeNull();
    expect(screen.queryByText("⋯")).toBeNull();
  });

  it("clicking a card only opens it — a paused card is not eagerly resumed", () => {
    const onSelect = vi.fn();
    const onResume = vi.fn();
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "a1", name: "live one", activity: "active" }),
          makeSession({ id: "r1", name: "resume me", activity: "inactive" }),
        ]}
        openTaskCounts={{}}
        onSelect={onSelect}
        onResume={onResume}
      />,
    );

    // Active card: opens, no resume (it's already running).
    fireEvent.click(screen.getByText("live one"));
    expect(onSelect).toHaveBeenCalledWith("a1");

    // Paused card: opens read-only (so the summary shows / it wakes on send) —
    // resume is the explicit ▶ pre-warm button, not a side effect of opening.
    fireEvent.click(screen.getByText("resume me"));
    expect(onSelect).toHaveBeenCalledWith("r1");
    expect(onResume).not.toHaveBeenCalled();
  });

  it("pressing Enter on a paused row opens it without resuming (keyboard-driven)", () => {
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
    const row = screen.getByText("resume me").closest('[role="button"]')!;
    fireEvent.keyDown(row, { key: "Enter" });
    expect(onSelect).toHaveBeenCalledWith("r1");
    expect(onResume).not.toHaveBeenCalled();
  });

  it("shows a Close (pause) affordance for active and inactive sessions alike, wired to onClose", () => {
    // Close/terminate is safe against the Supervisor in every activity
    // state, and the activity record can drift from the real child state —
    // so it must never be hidden based on that record alone (unlike Resume,
    // which stays gated to non-active sessions above).
    const onClose = vi.fn();
    const onSelect = vi.fn();
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "a1", name: "live one", activity: "active" }),
          makeSession({ id: "r1", name: "resume me", activity: "inactive" }),
        ]}
        openTaskCounts={{}}
        onSelect={onSelect}
        onClose={onClose}
      />,
    );

    expect(screen.getByRole("button", { name: "Close live one" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Close resume me" })).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Close resume me" }));
    expect(onClose).toHaveBeenCalledWith("r1");
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("action cluster exposes labeled resume and archive buttons, grouped under an aria-label", () => {
    render(
      <SessionRail
        sessions={[makeSession({ id: "r1", name: "resume me", activity: "inactive" })]}
        openTaskCounts={{}}
        onResume={vi.fn()}
      />,
    );
    const cluster = screen.getByRole("group", { name: "resume me actions" });
    expect(within(cluster).getByRole("button", { name: "Resume resume me" })).toBeTruthy();
    expect(within(cluster).getByRole("button", { name: "Archive resume me" })).toBeTruthy();
  });

  it("clicking Archive opens the shared confirm dialog; confirming targets the right row, canceling calls neither", () => {
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

    fireEvent.click(screen.getByRole("button", { name: "Archive delete me" }));

    const dialog = screen.getByRole("alertdialog");
    expect(dialog.textContent).toContain("event log survive");

    fireEvent.click(screen.getByText("Cancel"));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(onDelete).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Archive delete me" }));
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
    expect(screen.queryByRole("button", { name: /to link selection$/ })).toBeNull();
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
    // Holder row gets a link-toggle button; the nested subject row's badge
    // opens its link options instead.
    expect(screen.getByRole("button", { name: "Add investigator to link selection" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Link options for payments refactor" })).toBeTruthy();
  });

  it("shows a pending-restart pill on a leader link whose orchestrate grant hasn't materialized yet", () => {
    FLAGS.linkedSessions = true;
    const pendingLink = makeLink({ ...link, orchestrate_status: "pending_restart" });
    render(<SessionRail sessions={[lead, a]} openTaskCounts={{}} links={[pendingLink]} />);
    expect(screen.getByText("TOOLS ON RESTART")).toBeTruthy();
  });

  it("shows a no-tools pill on a leader link whose provider can't support orchestrate at all", () => {
    FLAGS.linkedSessions = true;
    const unsupportedLink = makeLink({ ...link, orchestrate_status: "unsupported" });
    render(<SessionRail sessions={[lead, a]} openTaskCounts={{}} links={[unsupportedLink]} />);
    expect(screen.getByText("NO TOOLS")).toBeTruthy();
  });

  it("renders no extra pill once the orchestrate grant is active", () => {
    FLAGS.linkedSessions = true;
    const activeLink = makeLink({ ...link, orchestrate_status: "active" });
    render(<SessionRail sessions={[lead, a]} openTaskCounts={{}} links={[activeLink]} />);
    expect(screen.queryByText("TOOLS ON RESTART")).toBeNull();
    expect(screen.queryByText("NO TOOLS")).toBeNull();
  });

  it("clicking a card's link action enters link mode with it as the anchor and shows a floating bar", () => {
    FLAGS.linkedSessions = true;
    const b = makeSession({ id: "b", name: "gateway hardening" });
    render(<SessionRail sessions={[lead, a, b]} openTaskCounts={{}} />);

    expect(screen.queryByText(/selected$/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Add investigator to link selection" }));

    expect(screen.getByText("1 selected")).toBeTruthy();
    expect(screen.getByText("Anchor")).toBeTruthy();
    // The anchor no longer exposes its own link-start button; the bar's
    // "Choose mode" advance is disabled until a second session joins.
    expect(screen.queryByRole("button", { name: "Add investigator to link selection" })).toBeNull();
    expect(
      (screen.getByRole("button", { name: "Choose mode" }) as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  it("tapping a same-workspace card in link mode adds it to the selection; tapping again removes it", () => {
    FLAGS.linkedSessions = true;
    const b = makeSession({ id: "b", name: "gateway hardening" });
    const onSelect = vi.fn();
    render(<SessionRail sessions={[lead, a, b]} openTaskCounts={{}} onSelect={onSelect} />);

    fireEvent.click(screen.getByRole("button", { name: "Add investigator to link selection" }));
    fireEvent.click(screen.getByText("gateway hardening"));

    expect(screen.getByText("2 selected")).toBeTruthy();
    expect(
      (
        screen.getByRole("checkbox", {
          name: "Remove gateway hardening from link selection",
        }) as HTMLInputElement
      ).checked,
    ).toBe(true);
    expect(
      (screen.getByRole("button", { name: "Choose mode" }) as HTMLButtonElement).disabled,
    ).toBe(false);
    // Tapping a card to add it to the selection must not also navigate to it.
    expect(onSelect).not.toHaveBeenCalled();

    fireEvent.click(screen.getByText("gateway hardening"));
    expect(screen.getByText("1 selected")).toBeTruthy();
  });

  it("dims cross-workspace cards during link mode so they cannot be added", () => {
    FLAGS.linkedSessions = true;
    const sameWorkspace = makeSession({ id: "b", name: "gateway hardening", workspace: "repo-a" });
    const otherWorkspace = makeSession({ id: "c", name: "unrelated repo work", workspace: "repo-b" });
    const anchor = makeSession({ id: "lead2", name: "investigator two", workspace: "repo-a" });
    render(
      <SessionRail sessions={[anchor, sameWorkspace, otherWorkspace]} openTaskCounts={{}} />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Add investigator two to link selection" }));

    expect(screen.getByText("different workspace")).toBeTruthy();
    expect(
      screen.queryByRole("checkbox", { name: "Add unrelated repo work to link selection" }),
    ).toBeNull();

    fireEvent.click(screen.getByText("unrelated repo work"));
    expect(screen.getByText("1 selected")).toBeTruthy();

    fireEvent.click(screen.getByText("gateway hardening"));
    expect(screen.getByText("2 selected")).toBeTruthy();
  });

  it("Cancel exits link mode and restores the normal per-row link action", () => {
    FLAGS.linkedSessions = true;
    const b = makeSession({ id: "b", name: "gateway hardening" });
    render(<SessionRail sessions={[lead, a, b]} openTaskCounts={{}} />);

    fireEvent.click(screen.getByRole("button", { name: "Add investigator to link selection" }));
    fireEvent.click(screen.getByText("gateway hardening"));
    fireEvent.click(screen.getByText("Cancel"));

    expect(screen.queryByText(/selected$/)).toBeNull();
    expect(screen.getByRole("button", { name: "Add investigator to link selection" })).toBeTruthy();
  });

  it("opens the chooser dialog and creates a link via the router client", async () => {
    FLAGS.linkedSessions = true;
    const b = makeSession({ id: "b", name: "gateway hardening" });
    const client = new FakeRouterClient();
    const createLinkSpy = vi.spyOn(client, "createLink");
    render(
      <SessionRail sessions={[lead, b]} openTaskCounts={{}} routerClient={client} />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Add investigator to link selection" }));
    fireEvent.click(screen.getByText("gateway hardening"));
    fireEvent.click(screen.getByRole("button", { name: "Choose mode" }));

    expect(screen.getByRole("dialog")).toBeTruthy();
    fireEvent.click(screen.getByText("Create 1 link"));

    await waitFor(() => expect(createLinkSpy).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    // Creating resets link mode entirely.
    expect(screen.queryByText(/selected$/)).toBeNull();
  });

  it("offers Unlink only from the nested subject row's link options, and confirming deletes the link", async () => {
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

    expect(screen.queryByRole("button", { name: "Link options for investigator" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Link options for payments refactor" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Unlink…" }));

    const dialog = screen.getByRole("alertdialog");
    expect(dialog.textContent).toContain("payments refactor");
    fireEvent.click(within(dialog).getByText("Unlink"));

    await waitFor(() => expect(deleteLinkSpy).toHaveBeenCalledWith("l1"));
  });

  it("edits a nested subject row's update policy from its link options", async () => {
    FLAGS.linkedSessions = true;
    const client = new FakeRouterClient();
    const updateLinkSpy = vi.spyOn(client, "updateLink");
    render(
      <SessionRail sessions={[lead, a]} openTaskCounts={{}} links={[link]} routerClient={client} />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Link options for payments refactor" }));
    fireEvent.click(screen.getByText("Catch up on request"));

    await waitFor(() =>
      expect(updateLinkSpy).toHaveBeenCalledWith("l1", {
        granting_session_id: "lead",
        subject_session_id: "a",
        grants: ["read", "inject", "orchestrate"],
        cadence: "manual_only",
      }),
    );
  });

  it("flags a manual_only link on the nested row's badge", () => {
    FLAGS.linkedSessions = true;
    const manualLink = makeLink({ ...link, cadence: "manual_only" });
    render(<SessionRail sessions={[lead, a]} openTaskCounts={{}} links={[manualLink]} />);
    expect(
      screen.getByRole("button", { name: "Link options for payments refactor" }).textContent,
    ).toContain("MANUAL");
  });
});

/** Fixed local reference point: Monday 28 Sep 2026, 13:40:00. */
const NOW = new Date(2026, 8, 28, 13, 40, 0).getTime();
const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;
const isoAgo = (ms: number) => new Date(NOW - ms).toISOString();

function stampOf(name: string): HTMLElement {
  const row = screen.getByText(name).closest('[role="button"]')!;
  return row.querySelector<HTMLElement>('[class*="when"]')!;
}

function footerOf(name: string): HTMLElement {
  const row = screen.getByText(name).closest('[role="button"]')!;
  return row.querySelector<HTMLElement>('[class*="stamps"]')!;
}

describe("SessionRail timestamps", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("shows a relative last-activity stamp on every name row, across the formatter's buckets", () => {
    vi.useFakeTimers({ now: NOW });
    const cards: [string, number][] = [
      ["sub-minute", 30_000],
      ["minutes", 14 * MINUTE],
      ["today", 5 * HOUR],
      ["this week", 3 * DAY],
      ["older", 10 * DAY],
    ];
    render(
      <SessionRail
        sessions={cards.map(([name, age], i) =>
          makeSession({ id: `t${i}`, name, updated_at: isoAgo(age) }),
        )}
        openTaskCounts={{}}
      />,
    );

    expect(stampOf("sub-minute").textContent).toBe("now");
    expect(stampOf("minutes").textContent).toBe("14m");
    expect(stampOf("today").textContent).toBe("08:40");
    expect(stampOf("this week").textContent).toMatch(/^\w{3,} 13:40$/);
    expect(stampOf("older").textContent).toMatch(/^18 \w{3,}$/);
  });

  it("puts the full absolute value in the stamp's title", () => {
    vi.useFakeTimers({ now: NOW });
    render(
      <SessionRail
        sessions={[makeSession({ name: "titled", updated_at: isoAgo(14 * MINUTE) })]}
        openTaskCounts={{}}
      />,
    );
    expect(stampOf("titled").title).toBe("Last activity 2026-09-28 13:26:00");
  });

  it("calls the stamp last activity, never last reply, while it is still updated_at", () => {
    vi.useFakeTimers({ now: NOW });
    render(
      <SessionRail
        sessions={[makeSession({ name: "honest", updated_at: isoAgo(14 * MINUTE) })]}
        openTaskCounts={{}}
      />,
    );
    expect(footerOf("honest").textContent).toContain("last activity");
    expect(stampOf("honest").title).toMatch(/^Last activity /);
    expect(screen.queryByText(/last reply/i)).toBeNull();
    expect(screen.queryByText(/last response/i)).toBeNull();
  });

  it("renders the started / last-activity footer on every card, hovered or not, so the reveal cannot reflow the rail", () => {
    vi.useFakeTimers({ now: NOW });
    render(
      <SessionRail
        sessions={[
          makeSession({
            id: "a",
            name: "resting",
            created_at: isoAgo(10 * DAY),
            updated_at: isoAgo(14 * MINUTE),
          }),
          makeSession({ id: "b", name: "chosen", created_at: isoAgo(5 * HOUR) }),
        ]}
        selectedId="b"
        openTaskCounts={{}}
      />,
    );

    // Mounted for the unselected, un-hovered card too: nothing is added to the
    // layout when the pointer arrives.
    const resting = footerOf("resting");
    expect(resting.textContent).toMatch(/^started 18 \w{3,}\s*·\s*last activity today 13:26$/);
    const beforeHover = resting.outerHTML;
    fireEvent.mouseEnter(screen.getByText("resting").closest('[role="button"]')!);
    expect(footerOf("resting").outerHTML).toBe(beforeHover);

    expect(footerOf("chosen").textContent).toContain("started today 08:40");
  });

  it("reveals that footer with opacity alone — no display or height change", () => {
    // Read as text: the reveal is a CSS fact, and jsdom/happy-dom has no layout
    // to measure. Path is relative to the vitest root (ui/).
    const css = readFileSync("src/features/sessions/rail/SessionRail.module.css", "utf8");
    const rest = css.match(/\.stamps \{([^}]*)\}/)![1];
    expect(rest).toContain("opacity: 0");
    expect(rest).toMatch(/transition:\s*opacity/);
    // Hidden by opacity, not by removal: no display:none and no collapsed box.
    expect(rest).not.toMatch(/display:\s*none|(max-)?height:\s*0/);

    const reveal = css.match(/\.row:hover \.stamps,[^{]*\{([^}]*)\}/)![1];
    expect(reveal.trim()).toBe("opacity: 1;");
    expect(css).toMatch(/\.row:focus-within \.stamps/);
    expect(css).toMatch(/\.selected \.stamps/);
  });

  it("drives every card's relative label from one shared interval", async () => {
    vi.useFakeTimers({ now: NOW });
    const { unmount } = render(
      <SessionRail
        sessions={[
          makeSession({ id: "a", name: "one", updated_at: isoAgo(50_000) }),
          makeSession({ id: "b", name: "two", updated_at: isoAgo(50_000) }),
          makeSession({ id: "c", name: "three", updated_at: isoAgo(50_000) }),
        ]}
        openTaskCounts={{}}
      />,
    );

    expect(vi.getTimerCount()).toBe(1);
    expect(stampOf("one").textContent).toBe("now");

    await act(async () => {
      vi.advanceTimersByTime(30_000);
    });

    expect(vi.getTimerCount()).toBe(1);
    for (const name of ["one", "two", "three"]) {
      expect(stampOf(name).textContent).toBe("1m");
    }

    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});

/** The card's status line (mockup §1) — its role="status" region. */
function statusOf(name: string): HTMLElement {
  const row = screen.getByText(name).closest('[role="button"]')!;
  return row.querySelector<HTMLElement>('[role="status"]')!;
}

/**
 * The shape that drives card height: one base class per rendered line. Tone
 * modifiers are stripped — colour is exactly what a state is allowed to change.
 */
function lineShape(name: string): string[] {
  const row = screen.getByText(name).closest('[role="button"]')!;
  return [...row.children].map((child) => child.className.split(" ")[0].replace(/_[a-z0-9]+$/i, ""));
}

describe("SessionRail status line", () => {
  const live = makeSession({ id: "s1", name: "the session", activity: "active" });

  function renderWith(state: Partial<SessionAgentState>, session: Session = live, counts = {}) {
    render(
      <SessionRail
        sessions={[session]}
        openTaskCounts={counts}
        agentStates={{ [session.id]: { subagents: 0, ...state } }}
      />,
    );
  }

  it("renders 'Ready' with a muted glyph for a live session that has said nothing", () => {
    renderWith({});
    const status = statusOf("the session");
    expect(status.textContent).toBe("◦Ready");
    expect(status.className).toMatch(/status-idle/);
  });

  it("renders 'Sleeping · wakes on send' for an inactive session", () => {
    renderWith({}, makeSession({ id: "s1", name: "the session", activity: "inactive" }));
    expect(statusOf("the session").textContent).toBe("◦Sleeping · wakes on send");
  });

  it("renders 'Done · your turn' in the ok tone with a check", () => {
    renderWith({ done: true });
    const status = statusOf("the session");
    expect(status.textContent).toBe("✓Done · your turn");
    expect(status.className).toMatch(/status-done/);
    expect(statusOf("the session").querySelector('[class*="dot"]')).toBeNull();
  });

  it("spins — with no glyph — while a turn is in flight, and names its subagents", () => {
    renderWith({ workingSince: "2026-07-01T00:00:00Z", subagents: 3 });
    const status = statusOf("the session");
    expect(status.textContent).toContain("3 subagents running");
    expect(status.className).toMatch(/status-working/);
    const spinner = status.querySelector('[class*="statusSpinner"]')!;
    expect(spinner).toBeTruthy();
    // Decorative: colour and copy carry the state, the motion doesn't.
    expect(spinner.getAttribute("aria-hidden")).toBe("true");
  });

  it("renders a parked approval in the warn tone, naming the tool", () => {
    renderWith({ awaitingSince: "2026-07-01T00:00:00Z", awaitingTool: "Bash" });
    const status = statusOf("the session");
    expect(status.textContent).toContain("Needs approval · Bash");
    expect(status.className).toMatch(/status-needs/);
    expect(status.querySelector('[class*="statusGlyph"]')!.textContent).toBe("▲");
  });

  it("renders a crash in the bad tone, counting the blocked work it left behind", () => {
    renderWith({ crashed: true }, live, { s1: { running: 0, ready: 0, blocked: 2 } });
    const status = statusOf("the session");
    expect(status.textContent).toBe("✕Crashed · 2 blocked tasks");
    expect(status.className).toMatch(/status-bad/);
  });

  it("paints the leading dot from the status: bad for a crash, accent while working", () => {
    const { unmount } = render(
      <SessionRail
        sessions={[live]}
        openTaskCounts={{}}
        agentStates={{ s1: { subagents: 0, crashed: true } }}
      />,
    );
    expect(
      screen.getByText("the session").closest('[role="button"]')!.querySelector('[class*="dot-bad"]'),
    ).toBeTruthy();
    unmount();

    renderWith({ workingSince: "2026-07-01T00:00:00Z" });
    expect(
      screen.getByText("the session").closest('[role="button"]')!.querySelector('[class*="dot-work"]'),
    ).toBeTruthy();
  });

  it("holds the documented precedence — crash over approval over working over done", () => {
    const everything: SessionAgentState = {
      crashed: true,
      awaitingSince: "2026-07-01T00:00:00Z",
      awaitingTool: "Bash",
      workingSince: "2026-07-01T00:00:00Z",
      subagents: 2,
      done: true,
    };
    const { rerender } = render(
      <SessionRail sessions={[live]} openTaskCounts={{}} agentStates={{ s1: everything }} />,
    );
    expect(statusOf("the session").textContent).toContain("Crashed");

    rerender(
      <SessionRail
        sessions={[live]}
        openTaskCounts={{}}
        agentStates={{ s1: { ...everything, crashed: false } }}
      />,
    );
    expect(statusOf("the session").textContent).toContain("Needs approval");

    rerender(
      <SessionRail
        sessions={[live]}
        openTaskCounts={{}}
        agentStates={{ s1: { ...everything, crashed: false, awaitingSince: undefined } }}
      />,
    );
    expect(statusOf("the session").textContent).toContain("2 subagents running");

    rerender(
      <SessionRail
        sessions={[live]}
        openTaskCounts={{}}
        agentStates={{
          s1: { ...everything, crashed: false, awaitingSince: undefined, workingSince: undefined },
        }}
      />,
    );
    expect(statusOf("the session").textContent).toBe("✓Done · your turn");
  });

  it("renders the same line shape in every state, so card height can't change with it", () => {
    const states: SessionAgentState[] = [
      { subagents: 0 },
      { subagents: 0, done: true },
      { subagents: 0, workingSince: "2026-07-01T00:00:00Z" },
      { subagents: 3, workingSince: "2026-07-01T00:00:00Z" },
      { subagents: 0, awaitingSince: "2026-07-01T00:00:00Z", awaitingTool: "Bash" },
      { subagents: 0, crashed: true },
    ];
    const shapes = states.map((state) => {
      const { unmount } = render(
        <SessionRail sessions={[live]} openTaskCounts={{}} agentStates={{ s1: state }} />,
      );
      const shape = lineShape("the session");
      unmount();
      return shape;
    });
    for (const shape of shapes) expect(shape).toEqual(shapes[0]);
  });

  it("never reorders a card when its status changes", () => {
    const sessions = [
      makeSession({ id: "a", name: "alpha", updated_at: "2026-07-01T10:00:00Z" }),
      makeSession({ id: "b", name: "bravo", updated_at: "2026-07-01T09:00:00Z" }),
      makeSession({ id: "c", name: "charlie", updated_at: "2026-07-01T08:00:00Z" }),
    ];
    const order = () =>
      [...screen.getByLabelText("Session list").querySelectorAll('div[role="button"]')].map(
        (row) => row.querySelector('[class*="name"]')!.textContent,
      );
    const { rerender } = render(<SessionRail sessions={sessions} openTaskCounts={{}} />);
    expect(order()).toEqual(["alpha", "bravo", "charlie"]);

    rerender(
      <SessionRail
        sessions={sessions}
        openTaskCounts={{}}
        agentStates={{
          c: { subagents: 1, workingSince: "2026-07-01T00:00:00Z" },
          a: { subagents: 0, done: true },
        }}
      />,
    );
    expect(order()).toEqual(["alpha", "bravo", "charlie"]);
  });

  it("counts a live state's elapsed time off the shared 1s tick", () => {
    vi.useFakeTimers({ now: Date.parse("2026-07-01T00:00:04Z") });
    try {
      renderWith({ workingSince: "2026-07-01T00:00:00Z" });
      expect(statusOf("the session").textContent).toBe("Working…4s");
      act(() => {
        vi.advanceTimersByTime(60_000);
      });
      expect(statusOf("the session").textContent).toBe("Working…1m");
    } finally {
      vi.useRealTimers();
    }
  });

  it("shows no counter for a state that isn't counting", () => {
    renderWith({ done: true });
    expect(statusOf("the session").querySelector('[class*="statusElapsed"]')).toBeNull();
  });
});

describe("SessionRail status line motion and truncation", () => {
  const css = () => readFileSync("src/features/sessions/rail/SessionRail.module.css", "utf8");

  it("spins by default and degrades to a static accent ring under prefers-reduced-motion", () => {
    const sheet = css();
    expect(sheet.match(/\.statusSpinner \{([^}]*)\}/)![1]).toMatch(/animation:\s*railSpin/);
    const reduced = sheet.match(/@media \(prefers-reduced-motion: reduce\) \{([\s\S]*?)\n\}/)![1];
    expect(reduced).toContain(".statusSpinner");
    // Still a ring, and still readable as one: the accent survives the motion.
    expect(reduced).toMatch(/border-color:\s*var\(--accent\)/);
  });

  it("truncates its one clause instead of wrapping, so the line can never grow", () => {
    const label = css().match(/\.statusLabel \{([^}]*)\}/)![1];
    expect(label).toMatch(/white-space:\s*nowrap/);
    expect(label).toMatch(/text-overflow:\s*ellipsis/);
    expect(label).toMatch(/overflow:\s*hidden/);
    // The line is reserved even when a state has nothing long to say.
    expect(css().match(/\n\.status \{([^}]*)\}/)![1]).toMatch(/min-height/);
  });
});

describe("SessionRail status subscription", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("uses one SSE connection however many sessions are listed", async () => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    const sessions = Array.from({ length: 12 }, (_, i) =>
      makeSession({ id: `s${i}`, name: `session ${i}`, updated_at: `2026-07-01T00:00:${String(i).padStart(2, "0")}Z` }),
    );

    function Harness() {
      const agentStates = useSessionAgentState();
      return <SessionRail sessions={sessions} openTaskCounts={{}} agentStates={agentStates} />;
    }
    render(<Harness />);

    const source = FakeEventSource.instances.filter((i) => !i.closed)[0];
    act(() => {
      source.emit(
        "session.turn_ended",
        JSON.stringify({
          seq: 1,
          timestamp: "2026-09-28T10:00:00Z",
          type: "session.turn_ended",
          actor: "agent",
          session_id: "s7",
        }),
      );
    });

    await waitFor(() => expect(statusOf("session 7").textContent).toBe("✓Done · your turn"));
    expect(statusOf("session 3").textContent).toBe("◦Ready");
    expect(FakeEventSource.instances.filter((i) => !i.closed)).toHaveLength(1);
  });
});
