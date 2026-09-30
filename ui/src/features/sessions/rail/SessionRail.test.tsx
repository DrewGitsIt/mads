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
    // Every card here models a session that has replied at least once: the
    // rail's stamp and its order both read last_turn_at.
    last_turn_at: "2026-07-01T00:00:00Z",
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
  it("renders one list with no Active/Recent headers at all", () => {
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "a1", name: "live one", activity: "active" }),
          makeSession({ id: "r1", name: "sleepy one", activity: "inactive" }),
        ]}
        openTaskCounts={{}}
      />,
    );
    expect(screen.queryByText("Active")).toBeNull();
    expect(screen.queryByText("Recent")).toBeNull();
    const cards = [...screen.getByLabelText("Session list").querySelectorAll('div[role="button"]')];
    expect(cards.map((c) => c.querySelector('[class*="name"]')!.textContent)).toEqual([
      "live one",
      "sleepy one",
    ]);
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

  // The cluster hangs outside the card's right edge (mockup §B): inside it, its
  // scrim covered the right 82-134px of the card, which is now the status
  // line, the context labels and the stamps.
  describe("action cluster anchor", () => {
    function fakeRect(box: Partial<DOMRect>): DOMRect {
      return {
        x: 0,
        y: 0,
        top: 0,
        left: 0,
        right: 0,
        bottom: 0,
        width: 0,
        height: 0,
        toJSON: () => ({}),
        ...box,
      } as DOMRect;
    }

    // The row looks its scroll viewport up as the nearest [data-rail-scroll]
    // ancestor, so the wrapper here stands in for SessionsSurface's aside —
    // which is the box that actually scrolls. The rail div SessionRail renders
    // inside it is unbounded and is never the viewport; see the SessionsSurface
    // test that pins the marker to the element owning overflow-y.
    function renderOneRow() {
      render(
        <div data-rail-scroll="">
          <SessionRail
            sessions={[makeSession({ id: "r1", name: "anchor me", activity: "inactive" })]}
            openTaskCounts={{}}
            onResume={vi.fn()}
          />
        </div>,
      );
      const row = screen.getByText("anchor me").closest('[role="button"]') as HTMLElement;
      const viewport = document.querySelector("[data-rail-scroll]") as HTMLElement;
      const cluster = screen.getByRole("group", { name: "anchor me actions" });
      // The viewport a scrolled row is judged against: 400px of visible rail.
      viewport.getBoundingClientRect = () => fakeRect({ top: 0, bottom: 400, left: 0, right: 312 });
      return { row, viewport, cluster };
    }

    it("places the cluster past the card's right edge, centred on the row", () => {
      const { row, cluster } = renderOneRow();
      row.getBoundingClientRect = () =>
        fakeRect({ top: 100, bottom: 200, height: 100, left: 12, right: 300, width: 288 });

      // Unanchored it has no coordinates, and the stylesheet keeps it hidden.
      expect(cluster.getAttribute("data-anchored")).toBeNull();
      expect(cluster.style.left).toBe("");

      fireEvent.mouseEnter(row);

      expect(cluster.getAttribute("data-anchored")).toBe("");
      expect(cluster.style.left).toBe("300px");
      expect(cluster.style.top).toBe("150px");
    });

    it("keyboard focus anchors it too, and it survives the pointer leaving a focused row", () => {
      const { row, cluster } = renderOneRow();
      row.getBoundingClientRect = () =>
        fakeRect({ top: 100, bottom: 200, height: 100, left: 12, right: 300, width: 288 });

      const resume = within(cluster).getByRole("button", { name: "Resume anchor me" });
      act(() => resume.focus());
      expect(document.activeElement).toBe(resume);
      expect(cluster.getAttribute("data-anchored")).toBe("");

      // :focus-within still holds it open, so the coordinates must not go.
      fireEvent.mouseLeave(row);
      expect(cluster.getAttribute("data-anchored")).toBe("");
    });

    it("follows its row as the rail scrolls, and lets go once the row leaves the viewport", () => {
      const { row, viewport, cluster } = renderOneRow();
      let box = { top: 100, bottom: 200, height: 100, left: 12, right: 300, width: 288 };
      row.getBoundingClientRect = () => fakeRect(box);

      fireEvent.mouseEnter(row);
      expect(cluster.style.top).toBe("150px");

      // Scrolled up by 80: the cluster moves with the card, never stranding
      // itself beside whichever row took its place.
      box = { ...box, top: 20, bottom: 120 };
      fireEvent.scroll(viewport);
      expect(cluster.style.top).toBe("70px");
      expect(cluster.getAttribute("data-anchored")).toBe("");

      // Scrolled clean out of the rail's viewport: dismissed rather than left
      // floating over the search field above it.
      box = { ...box, top: -300, bottom: -200 };
      fireEvent.scroll(viewport);
      expect(cluster.getAttribute("data-anchored")).toBeNull();
    });

    it("paints outside the card without taking any of its width or height", () => {
      const css = readFileSync("src/features/sessions/rail/SessionRail.module.css", "utf8");
      const rest = css.match(/\.actionCluster \{([^}]*)\}/)![1];
      // Fixed, not absolute: the rail scrolls, and a scroll container clips
      // both axes, so an absolutely positioned child cannot escape sideways.
      expect(rest).toMatch(/position:\s*fixed/);
      // A column, not a strip across the card, and no scrim over its text.
      expect(rest).toMatch(/flex-direction:\s*column/);
      expect(rest).not.toMatch(/linear-gradient/);
      // A transparent padding bridge, not a margin: the pointer must never
      // cross dead space between card and cluster or :hover drops.
      expect(rest).toMatch(/padding:\s*3px 4px 3px 8px/);
      expect(rest).not.toMatch(/margin-left/);
      // Hidden until the row's rect has been measured into an inline style.
      expect(css).toMatch(/\.row:hover \.actionCluster\[data-anchored\]/);
      expect(css).toMatch(/\.row:focus-within \.actionCluster\[data-anchored\]/);
    });

    // Hanging outside the rail means overlapping the conversation pane, whose
    // own chrome is z-indexed well above anything the rail needed before.
    // Neither the rail nor the pane opens a stacking context between those
    // rules and the root, so they compete directly and the column loses unless
    // it outranks them.
    it("stacks above the conversation pane's composer and activity dock", () => {
      function zIndexIn(file: string, rule: string): number {
        const css = readFileSync(file, "utf8");
        const body = css.match(new RegExp(`\\${rule} \\{([^}]*)\\}`))![1];
        return Number(body.match(/z-index:\s*(\d+)/)![1]);
      }

      const cluster = zIndexIn(
        "src/features/sessions/rail/SessionRail.module.css",
        ".actionCluster",
      );
      const composer = zIndexIn("src/conversation/ConversationTab.module.css", ".pinned");
      const dock = zIndexIn("src/conversation/activity/ActivityDock.module.css", ".dock");

      expect(composer).toBeGreaterThan(0);
      expect(dock).toBeGreaterThan(0);
      expect(cluster).toBeGreaterThan(composer);
      expect(cluster).toBeGreaterThan(dock);
      // But still under the command palette and the modal dialogs, which are
      // meant to cover the rail rather than be poked through by it.
      expect(cluster).toBeLessThan(
        zIndexIn("src/features/command-palette/CommandPalette.module.css", ".scrim"),
      );
      expect(cluster).toBeLessThan(
        zIndexIn("src/components/ConfirmDialog/ConfirmDialog.module.css", ".backdrop"),
      );
    });
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

  it("shows a tools-failed pill, with the reason as its tooltip, when the tools couldn't start", () => {
    FLAGS.linkedSessions = true;
    const failedLink = makeLink({
      ...link,
      orchestrate_status: "failed",
      orchestrate_error: 'exec: "mads": executable file not found in $PATH',
    });
    render(<SessionRail sessions={[lead, a]} openTaskCounts={{}} links={[failedLink]} />);
    const pill = screen.getByText("TOOLS FAILED");
    expect(pill.getAttribute("title")).toMatch(/not found in \$PATH/);
    expect(screen.queryByText("TOOLS ON RESTART")).toBeNull();
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

/** The status line's clause alone, without whatever the elapsed counter reads. */
function clauseOf(name: string): string {
  const row = screen.getByText(name).closest('[role="button"]')!;
  return row.querySelector<HTMLElement>('[class*="statusLabel"]')!.textContent!;
}

describe("SessionRail timestamps", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("shows a relative last-reply stamp on every name row, across the formatter's buckets", () => {
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
          makeSession({ id: `t${i}`, name, last_turn_at: isoAgo(age) }),
        )}
        openTaskCounts={{}}
      />,
    );

    expect(stampOf("sub-minute").textContent).toBe("now");
    expect(stampOf("minutes").textContent).toBe("14m");
    expect(stampOf("today").textContent).toBe("08:40");

    // The last two are a day cold, so they sit in the collapsed "Earlier" tail.
    fireEvent.click(screen.getByRole("button", { name: /Earlier/ }));
    expect(stampOf("this week").textContent).toMatch(/^\w{3,} 13:40$/);
    expect(stampOf("older").textContent).toMatch(/^18 \w{3,}$/);
  });

  // One stamp carries both absolutes, because the card no longer has a footer
  // line to spell them out — and that footer showed nothing until hovered.
  it("puts both absolute values in the stamp's title", () => {
    vi.useFakeTimers({ now: NOW });
    render(
      <SessionRail
        sessions={[
          makeSession({
            name: "titled",
            created_at: isoAgo(10 * DAY),
            last_turn_at: isoAgo(14 * MINUTE),
          }),
        ]}
        openTaskCounts={{}}
      />,
    );
    expect(stampOf("titled").title).toBe(
      "Last reply 2026-09-28 13:26:00 · Started 2026-09-18 13:40:00",
    );
  });

  it("calls the stamp last reply, and reads it off last_turn_at — not updated_at", () => {
    vi.useFakeTimers({ now: NOW });
    render(
      <SessionRail
        sessions={[
          makeSession({
            name: "honest",
            last_turn_at: isoAgo(14 * MINUTE),
            // A rename landed a second ago. It is not a reply, and the card
            // must not report it as one.
            updated_at: isoAgo(1000),
          }),
        ]}
        openTaskCounts={{}}
      />,
    );
    expect(stampOf("honest").textContent).toBe("14m");
    expect(stampOf("honest").title).toContain("Last reply 2026-09-28 13:26:00");
    expect(screen.queryByText(/last activity/i)).toBeNull();
  });

  it("reads 'new' rather than dressing a creation time up as a reply", () => {
    vi.useFakeTimers({ now: NOW });
    render(
      <SessionRail
        sessions={[
          makeSession({
            name: "fresh",
            created_at: isoAgo(30_000),
            // A rename a second ago. Neither this nor created_at is a reply,
            // and neither may appear in the slot the eye reads as one.
            updated_at: isoAgo(1000),
            last_turn_at: undefined,
          }),
        ]}
        openTaskCounts={{}}
      />,
    );
    expect(stampOf("fresh").textContent).toBe("new");
    expect(stampOf("fresh").title).toBe("No reply yet · Started 2026-09-28 13:39:30");
  });

  it("carries no reserved footer line, so nothing on the card is blank at rest", () => {
    vi.useFakeTimers({ now: NOW });
    render(
      <SessionRail
        sessions={[
          makeSession({
            id: "a",
            name: "resting",
            created_at: isoAgo(10 * DAY),
            last_turn_at: isoAgo(14 * MINUTE),
          }),
          makeSession({
            id: "b",
            name: "chosen",
            created_at: isoAgo(5 * HOUR),
            last_turn_at: isoAgo(2 * HOUR),
          }),
        ]}
        selectedId="b"
        openTaskCounts={{}}
      />,
    );

    // The footer this replaced was mounted on every card at opacity 0 — 16px
    // of height showing nothing until the pointer arrived, half of it
    // repeating the name-row stamp in longer words.
    const row = screen.getByText("resting").closest('[role="button"]')!;
    expect(row.querySelector('[class*="stamps"]')).toBeNull();
    expect(row.textContent).not.toMatch(/started|last reply/i);

    // And the reveal it needed is gone with it: hovering changes no text on
    // the card. (It still anchors the action cluster, which is why this reads
    // the card's words rather than diffing its markup.)
    const before = row.textContent;
    fireEvent.mouseEnter(row);
    expect(row.textContent).toBe(before);

    // Both stamps still reachable, on the one element that shows a time.
    expect(stampOf("resting").title).toBe(
      "Last reply 2026-09-28 13:26:00 · Started 2026-09-18 13:40:00",
    );
    expect(stampOf("chosen").title).toContain("Started 2026-09-28 08:40:00");
  });

  it("keeps no stamps rule in the stylesheet either", () => {
    // Path is relative to the vitest root (ui/).
    const css = readFileSync("src/features/sessions/rail/SessionRail.module.css", "utf8");
    expect(css).not.toMatch(/\.stamps\b/);
    expect(css).not.toMatch(/\.stampSep\b/);
  });

  it("drives every card's relative label from one shared interval", async () => {
    vi.useFakeTimers({ now: NOW });
    const { unmount } = render(
      <SessionRail
        sessions={[
          makeSession({ id: "a", name: "one", last_turn_at: isoAgo(50_000) }),
          makeSession({ id: "b", name: "two", last_turn_at: isoAgo(50_000) }),
          makeSession({ id: "c", name: "three", last_turn_at: isoAgo(50_000) }),
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
      makeSession({ id: "a", name: "alpha", last_turn_at: "2026-07-01T10:00:00Z" }),
      makeSession({ id: "b", name: "bravo", last_turn_at: "2026-07-01T09:00:00Z" }),
      makeSession({ id: "c", name: "charlie", last_turn_at: "2026-07-01T08:00:00Z" }),
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
    vi.useRealTimers();
  });

  /** The rail wired to its own hook, exactly as SessionsSurface wires it. */
  function Harness({ sessions }: { sessions: Session[] }) {
    const agentStates = useSessionAgentState(sessions);
    return <SessionRail sessions={sessions} openTaskCounts={{}} agentStates={agentStates} />;
  }

  function emitAgentState(sessionId: string, payload: unknown, seq = 1) {
    const source = FakeEventSource.instances.filter((i) => !i.closed)[0];
    act(() => {
      source.emit(
        "session.agent_state",
        JSON.stringify({
          seq,
          timestamp: "2026-09-28T13:40:00Z",
          type: "session.agent_state",
          actor: "agent",
          session_id: sessionId,
          payload,
        }),
      );
    });
  }

  it("uses one SSE connection however many sessions are listed", async () => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    const sessions = Array.from({ length: 12 }, (_, i) =>
      makeSession({ id: `s${i}`, name: `session ${i}`, last_turn_at: `2026-07-01T00:00:${String(i).padStart(2, "0")}Z` }),
    );
    render(<Harness sessions={sessions} />);

    // The turn's end — reported whether or not the turn produced a digest
    // delta, which is what session.turn_ended could not promise.
    emitAgentState("s7", { state: "idle", since: "2026-09-28T13:40:00Z" });

    await waitFor(() => expect(statusOf("session 7").textContent).toBe("✓Done · your turn"));
    expect(statusOf("session 3").textContent).toBe("◦Ready");
    expect(FakeEventSource.instances.filter((i) => !i.closed)).toHaveLength(1);
  });

  it("paints the server-built clause a live turn reports, not a generic 'Working…'", async () => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    const sessions = [makeSession({ id: "s1", name: "live one" })];
    render(<Harness sessions={sessions} />);

    emitAgentState("s1", { state: "working", label: "Working…", since: "2026-09-28T13:40:00Z" });
    // The elapsed counter rides the real clock here, so only the clause is read.
    await waitFor(() => expect(clauseOf("live one")).toBe("Working…"));

    // The same string the open conversation's working-indicator ticker shows
    // for this call: one server-side enrichment feeds both surfaces.
    emitAgentState(
      "s1",
      { state: "working", label: "Running bash · make check", since: "2026-09-28T13:40:00Z" },
      2,
    );
    await waitFor(() => expect(clauseOf("live one")).toBe("Running bash · make check"));
    emitAgentState("s1", { state: "working", label: "Thinking…", since: "2026-09-28T13:40:00Z" }, 3);
    await waitFor(() => expect(clauseOf("live one")).toBe("Thinking…"));
  });

  it("renders a live turn straight from the session record — the reload-mid-turn case", async () => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    vi.useFakeTimers({ now: NOW });
    // No events at all: this browser has just loaded and the turn started
    // before it was listening. The record's mirror is all it has.
    const mid = makeSession({
      id: "s1",
      name: "mid-turn one",
      agent_state: {
        state: "working",
        label: "Running bash · make check",
        since: new Date(NOW - 18_000).toISOString(),
      },
    });
    const { rerender } = render(<Harness sessions={[mid]} />);

    // No waiting and no event: the first paint already carries the live clause,
    // counter included — the turn is 18s old and the card says so.
    expect(statusOf("mid-turn one").textContent).toBe("Running bash · make check18s");

    // The counter spans the TURN, not the tool — the label changes under it and
    // it keeps counting up from the turn's own start (WorkingIndicator's rule).
    rerender(
      <Harness
        sessions={[{ ...mid, agent_state: { ...mid.agent_state!, label: "Thinking…" } }]}
      />,
    );
    await act(async () => {
      vi.advanceTimersByTime(2000);
    });
    expect(statusOf("mid-turn one").textContent).toBe("Thinking…20s");
  });

  it("reads a sleeping session's leftover working mirror as idle, never as a live turn", async () => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    // Only a live Runner clears agent_state, so a hard-killed process leaves
    // its last working state on the record. That card must not spin forever.
    const stale = makeSession({
      id: "s1",
      name: "killed one",
      activity: "inactive",
      agent_state: { state: "working", label: "Running bash · make check", since: "2026-09-28T09:00:00Z" },
    });
    render(<Harness sessions={[stale]} />);
    await waitFor(() => expect(statusOf("killed one").textContent).toBe("◦Sleeping · wakes on send"));
  });
});

/** The rail's rendered card order, top to bottom. */
function cardNames(): string[] {
  return [...screen.getByLabelText("Session list").querySelectorAll('div[role="button"]')].map(
    (c) => c.querySelector('[class*="name"]')!.textContent!,
  );
}

function chip(label: string): HTMLElement {
  const bar = screen.getByRole("group", { name: "Filter sessions" });
  return [...bar.querySelectorAll("button")].find((b) => b.textContent!.startsWith(label))!;
}

describe("SessionRail filter chips", () => {
  // One of each bucket, most recent first, all inside the last day.
  const working = makeSession({ id: "w", name: "working one", last_turn_at: isoAgo(1 * MINUTE) });
  const parked = makeSession({ id: "p", name: "parked one", last_turn_at: isoAgo(2 * MINUTE) });
  const crashed = makeSession({ id: "x", name: "crashed one", last_turn_at: isoAgo(3 * MINUTE) });
  const ready = makeSession({ id: "r", name: "ready one", last_turn_at: isoAgo(4 * MINUTE) });
  const sleeping = makeSession({
    id: "s",
    name: "sleeping one",
    activity: "inactive",
    last_turn_at: isoAgo(5 * MINUTE),
  });
  const agentStates: Record<string, SessionAgentState> = {
    w: { subagents: 0, workingSince: isoAgo(30_000) },
    p: { subagents: 0, awaitingSince: isoAgo(30_000), awaitingTool: "Bash" },
    x: { subagents: 0, crashed: true },
  };

  function renderRail() {
    vi.useFakeTimers({ now: NOW });
    render(
      <SessionRail
        sessions={[working, parked, crashed, ready, sleeping]}
        openTaskCounts={{}}
        agentStates={agentStates}
      />,
    );
  }

  afterEach(() => {
    vi.useRealTimers();
  });

  it("carries a count per chip, matching the status vocabulary the cards paint", () => {
    renderRail();
    expect(chip("All").textContent).toBe("All5");
    expect(chip("Working").textContent).toBe("Working1");
    // A crash and a parked approval are both "needs you".
    expect(chip("Needs you").textContent).toBe("Needs you2");
    // Ready and sleeping alike — neither is mid-turn.
    expect(chip("Idle").textContent).toBe("Idle2");
  });

  it("counts a quiet session's blocked work as needs-you, like the amber dot does", () => {
    vi.useFakeTimers({ now: NOW });
    render(
      <SessionRail
        sessions={[sleeping]}
        openTaskCounts={{ s: { running: 0, ready: 0, blocked: 1 } }}
      />,
    );
    expect(chip("Needs you").textContent).toBe("Needs you1");
    expect(chip("Idle").textContent).toBe("Idle0");
  });

  it("narrows the list to a chip's bucket without re-sorting the survivors", () => {
    renderRail();
    expect(cardNames()).toEqual([
      "working one",
      "parked one",
      "crashed one",
      "ready one",
      "sleeping one",
    ]);

    fireEvent.click(chip("Needs you"));
    expect(cardNames()).toEqual(["parked one", "crashed one"]);
    expect(chip("Needs you").getAttribute("aria-pressed")).toBe("true");

    fireEvent.click(chip("Idle"));
    expect(cardNames()).toEqual(["ready one", "sleeping one"]);

    fireEvent.click(chip("All"));
    expect(cardNames()).toEqual([
      "working one",
      "parked one",
      "crashed one",
      "ready one",
      "sleeping one",
    ]);
  });

  it("leaves a pending reorder pending — a chip narrows the order, it never applies one", () => {
    vi.useFakeTimers({ now: NOW });
    const a = makeSession({ id: "a", name: "alpha", last_turn_at: isoAgo(10 * MINUTE) });
    const b = makeSession({ id: "b", name: "bravo", last_turn_at: isoAgo(20 * MINUTE) });
    const c = makeSession({ id: "c", name: "charlie", last_turn_at: isoAgo(30 * MINUTE) });
    const { rerender } = render(<SessionRail sessions={[a, b, c]} openTaskCounts={{}} />);
    fireEvent.mouseEnter(screen.getByLabelText("Session list"));

    const risen = { ...c, last_turn_at: isoAgo(1 * MINUTE) };
    rerender(<SessionRail sessions={[a, b, risen]} openTaskCounts={{}} />);
    expect(cardNames()).toEqual(["alpha", "bravo", "charlie"]);
    expect(screen.getByRole("button", { name: /session moved/ })).toBeTruthy();

    // Filtering is a view, not a settle: the survivors keep the held order and
    // the ghost is still the only thing offering to reorder them.
    fireEvent.click(chip("Idle"));
    expect(cardNames()).toEqual(["alpha", "bravo", "charlie"]);
    expect(screen.getByRole("button", { name: /session moved/ })).toBeTruthy();
  });

  it("holds the chosen chip — clicking it again is not a toggle back to All", () => {
    renderRail();
    fireEvent.click(chip("Working"));
    expect(cardNames()).toEqual(["working one"]);
    fireEvent.click(chip("Working"));
    expect(cardNames()).toEqual(["working one"]);
    expect(chip("All").getAttribute("aria-pressed")).toBe("false");
  });

  it("says so rather than showing an empty rail when a chip matches nothing", () => {
    vi.useFakeTimers({ now: NOW });
    render(<SessionRail sessions={[ready]} openTaskCounts={{}} />);
    fireEvent.click(chip("Working"));
    expect(cardNames()).toEqual([]);
    expect(screen.getByText("No sessions match this filter.")).toBeTruthy();
  });
});

describe("SessionRail — the Earlier tail", () => {
  const fresh = makeSession({ id: "f", name: "fresh one", last_turn_at: isoAgo(5 * MINUTE) });
  const yesterday = makeSession({ id: "y", name: "old one", last_turn_at: isoAgo(2 * DAY) });
  const lastWeek = makeSession({ id: "l", name: "ancient one", last_turn_at: isoAgo(9 * DAY) });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("folds everything a day cold behind a collapsed 'Earlier' toggle, and expands it", () => {
    vi.useFakeTimers({ now: NOW });
    render(<SessionRail sessions={[fresh, yesterday, lastWeek]} openTaskCounts={{}} />);

    expect(cardNames()).toEqual(["fresh one"]);
    const toggle = screen.getByRole("button", { name: /Earlier/ });
    expect(toggle.textContent).toContain("· 2");
    expect(toggle.getAttribute("aria-expanded")).toBe("false");

    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(cardNames()).toEqual(["fresh one", "old one", "ancient one"]);

    fireEvent.click(toggle);
    expect(cardNames()).toEqual(["fresh one"]);
  });

  it("renders no tail at all when nothing is recent — a fold with no head would hide the rail", () => {
    vi.useFakeTimers({ now: NOW });
    render(<SessionRail sessions={[yesterday, lastWeek]} openTaskCounts={{}} />);

    expect(screen.queryByRole("button", { name: /Earlier/ })).toBeNull();
    expect(cardNames()).toEqual(["old one", "ancient one"]);
  });

  it("folds the tail out of the active filter, not out of the session list", () => {
    vi.useFakeTimers({ now: NOW });
    render(
      <SessionRail
        sessions={[fresh, yesterday, lastWeek]}
        openTaskCounts={{}}
        agentStates={{ f: { subagents: 0, workingSince: isoAgo(30_000) } }}
      />,
    );

    // The tail's cards are still counted by the chips that describe them.
    expect(chip("All").textContent).toBe("All3");
    expect(chip("Idle").textContent).toBe("Idle2");

    // Filter to Idle and the working card — the only recent one — drops out, so
    // the tail becomes the whole list and unfolds itself.
    fireEvent.click(chip("Idle"));
    expect(screen.queryByRole("button", { name: /Earlier/ })).toBeNull();
    expect(cardNames()).toEqual(["old one", "ancient one"]);
  });
});

// ── #2b omni search ──────────────────────────────────────────────────────

function searchBox(): HTMLInputElement {
  return screen.getByLabelText("Search sessions") as HTMLInputElement;
}

/** The reason lines a card is printing — the acceptance criterion counts them. */
function reasonsOf(name: string): string[] {
  const row = screen.getByText(name).closest('[role="button"]')!;
  return [...row.querySelectorAll("[data-match-reason]")].map((el) => el.textContent!);
}

function cards(): HTMLElement[] {
  return [
    ...screen.getByLabelText("Session list").querySelectorAll<HTMLElement>('div[role="button"]'),
  ];
}

describe("SessionRail omni search", () => {
  // The mockup's own fixture set. "rail readability pass" is the case that
  // decides the field list: its workspace is "Main work", which shares no
  // substring with ~/repo/mads-ui, so both have to be indexed.
  const goose = makeSession({
    id: "g",
    name: "goose ACP transcript gap",
    workspace: "mads",
    cwd: "~/repo/mads",
    worktree_branch: "wt/goose-acp",
    last_turn_at: isoAgo(1 * MINUTE),
  });
  const electron = makeSession({
    id: "e",
    name: "electron self-heal reinstall",
    workspace: "mads",
    cwd: "~/repo/mads",
    last_turn_at: isoAgo(6 * MINUTE),
  });
  const retro = makeSession({
    id: "s",
    name: "retro: conversation UX",
    workspace: "sidecar",
    cwd: "~/repo/sidecar",
    last_turn_at: isoAgo(3 * HOUR),
  });
  const railUi = makeSession({
    id: "u",
    name: "rail readability pass",
    workspace: "Main work",
    cwd: "~/repo/mads-ui",
    last_turn_at: isoAgo(5 * HOUR),
  });
  const digest = makeSession({
    id: "d",
    name: "digest watcher backlog",
    workspace: "sidecar",
    cwd: "~/repo/sidecar",
    worktree_branch: "wt/digest",
    last_turn_at: isoAgo(2 * DAY),
  });
  const all = [goose, electron, retro, railUi, digest];

  function renderRail(props: Partial<React.ComponentProps<typeof SessionRail>> = {}) {
    vi.useFakeTimers({ now: NOW });
    return render(<SessionRail sessions={all} openTaskCounts={{}} {...props} />);
  }

  function type(text: string) {
    fireEvent.change(searchBox(), { target: { value: text } });
  }

  afterEach(() => {
    vi.useRealTimers();
  });

  it("matches on each of the four indexed fields", () => {
    renderRail();

    type("readability");
    expect(cardNames()).toEqual(["rail readability pass"]);

    type("Main");
    expect(cardNames()).toEqual(["rail readability pass"]);

    type("mads-ui");
    expect(cardNames()).toEqual(["rail readability pass"]);

    type("wt/goose");
    expect(cardNames()).toEqual(["goose ACP transcript gap"]);
  });

  it("finds a session whose workspace name shares no substring with its cwd", () => {
    renderRail();
    type("mads");
    // "Main work" contains no "mads" — this card is here on its directory alone.
    expect(cardNames()).toContain("rail readability pass");
  });

  it("ANDs whitespace-separated terms", () => {
    renderRail();
    type("goose acp");
    expect(cardNames()).toEqual(["goose ACP transcript gap"]);
    type("goose sidecar");
    expect(cardNames()).toEqual([]);
    expect(screen.getByText(/No session matches/)).toBeTruthy();
  });

  it("filters to one field when a scope token is used", () => {
    renderRail();

    type("mads");
    expect(cardNames()).toContain("rail readability pass");

    // dir: keeps it (its cwd is ~/repo/mads-ui), workspace: drops it ("Main work").
    type("dir:mads");
    expect(cardNames()).toContain("rail readability pass");
    type("workspace:mads");
    expect(cardNames()).not.toContain("rail readability pass");
    expect(cardNames()).toEqual(["goose ACP transcript gap", "electron self-heal reinstall"]);
  });

  it("offers a committable scope token for a matching workspace or directory", () => {
    renderRail();
    type("sidecar");

    const scope = screen.getByRole("group", { name: "Scope search" });
    const chips = [...scope.querySelectorAll("button")];
    expect(chips.map((c) => c.textContent)).toEqual([
      "workspace:sidecar2",
      "dir:~/repo/sidecar2",
    ]);

    fireEvent.click(chips[0]);
    expect(searchBox().value).toBe("workspace:sidecar");
    // A committed scope is not itself re-scopable, so the chip row retires.
    expect(screen.queryByRole("group", { name: "Scope search" })).toBeNull();
  });

  it("prints exactly one reason under a non-name match and none under a name match", () => {
    renderRail();

    type("mads");
    // On its directory — "Main work" and the name carry no "mads".
    expect(reasonsOf("rail readability pass")).toEqual(["dir~/repo/mads-ui"]);
    // On its workspace, which outranks its equally-matching directory.
    expect(reasonsOf("goose ACP transcript gap")).toEqual(["workspacemads"]);

    type("rail");
    expect(reasonsOf("rail readability pass")).toEqual([]);
  });

  it("reaches sessions folded into the collapsed Earlier tail", () => {
    renderRail();
    // At rest the two-day-old card is behind the fold.
    expect(cardNames()).not.toContain("digest watcher backlog");

    type("digest");
    expect(cardNames()).toEqual(["digest watcher backlog"]);
    // Search bypasses the view: no fold is drawn over a result list.
    expect(screen.queryByRole("button", { name: /Earlier/ })).toBeNull();
  });

  it("orders hits by relevance and restores the resting order identically on clear", () => {
    renderRail();
    const resting = cardNames();
    expect(resting).toEqual([
      "goose ACP transcript gap",
      "electron self-heal reinstall",
      "retro: conversation UX",
      "rail readability pass",
    ]);

    // "re" is a name prefix for "retro", a name substring for the other two
    // "…re…" names, and only a ~/repo hit for goose and the folded-away
    // digest card — which search reaches and relevance drops to the bottom.
    type("re");
    expect(cardNames()).toEqual([
      "retro: conversation UX",
      "electron self-heal reinstall",
      "rail readability pass",
      "goose ACP transcript gap",
      "digest watcher backlog",
    ]);

    fireEvent.click(screen.getByLabelText("Clear search"));
    expect(searchBox().value).toBe("");
    expect(cardNames()).toEqual(resting);
  });

  it("focuses the field on Cmd/Ctrl-K", () => {
    renderRail();
    expect(document.activeElement).not.toBe(searchBox());

    fireEvent.keyDown(document, { key: "k", metaKey: true });
    expect(document.activeElement).toBe(searchBox());

    searchBox().blur();
    fireEvent.keyDown(document, { key: "k", ctrlKey: true });
    expect(document.activeElement).toBe(searchBox());
  });

  it("does not fire Cmd/Ctrl-K while the composer has focus", () => {
    vi.useFakeTimers({ now: NOW });
    render(
      <>
        <textarea aria-label="composer" />
        <SessionRail sessions={all} openTaskCounts={{}} />
      </>,
    );
    const composer = screen.getByLabelText("composer");
    composer.focus();

    fireEvent.keyDown(document, { key: "k", metaKey: true });
    expect(document.activeElement).toBe(composer);
    expect(document.activeElement).not.toBe(searchBox());
  });

  it("walks the results with the arrow keys and comes back to the field", () => {
    renderRail();
    const box = searchBox();
    box.focus();
    type("re");

    fireEvent.keyDown(box, { key: "ArrowDown" });
    expect(document.activeElement).toBe(cards()[0]);

    fireEvent.keyDown(cards()[0], { key: "ArrowDown" });
    expect(document.activeElement).toBe(cards()[1]);

    fireEvent.keyDown(cards()[1], { key: "ArrowUp" });
    expect(document.activeElement).toBe(cards()[0]);

    fireEvent.keyDown(cards()[0], { key: "ArrowUp" });
    expect(document.activeElement).toBe(box);
  });

  it("opens the top hit on Enter", () => {
    const onSelect = vi.fn();
    renderRail({ onSelect });

    type("re");
    fireEvent.keyDown(searchBox(), { key: "Enter" });
    expect(onSelect).toHaveBeenCalledWith(retro.id);
  });

  it("clears on the first Escape and blurs on the second", () => {
    renderRail();
    const box = searchBox();
    box.focus();
    type("goose");

    fireEvent.keyDown(box, { key: "Escape" });
    expect(box.value).toBe("");
    expect(document.activeElement).toBe(box);

    fireEvent.keyDown(box, { key: "Escape" });
    expect(document.activeElement).not.toBe(box);
  });
});
