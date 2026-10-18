import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { DONE_OPEN_KEY, SessionRail, type RailLinkEditor } from "./SessionRail";
import { FLAGS } from "../../../featureFlags";
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
    // stamp and the Done order read last_turn_at. The active order does not —
    // it is created_at (railRankOf), so a test that cares about order sets that.
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

  it("says 'Idle · N blocked tasks' in the needs tone for an idle session with blocked work", () => {
    render(
      <SessionRail
        sessions={[makeSession({ id: "r1", activity: "inactive", name: "needs work" })]}
        openTaskCounts={{ r1: { running: 0, ready: 0, blocked: 1 } }}
      />,
    );
    const row = screen.getByText("needs work").closest('[role="button"]')!;
    const status = row.querySelector('[role="status"]')!;
    expect(status.textContent).toBe("Idle · 1 blocked task");
    expect(status.className).toMatch(/status-needs/);
    expect(row.querySelector('[class*="dot"]')).toBeNull();
    expect(screen.getByText("1 blocked")).toBeTruthy();
  });

  it("stays plain 'Idle' in the idle tone when the open work is only running/ready", () => {
    render(
      <SessionRail
        sessions={[makeSession({ id: "r1", activity: "inactive", name: "steady work" })]}
        openTaskCounts={{ r1: { running: 1, ready: 1, blocked: 0 } }}
      />,
    );
    const row = screen.getByText("steady work").closest('[role="button"]')!;
    const status = row.querySelector('[role="status"]')!;
    expect(status.textContent).toBe("Idle");
    expect(status.className).toMatch(/status-idle/);
  });

  it("renders only non-zero buckets, in running / ready / blocked order", () => {
    render(
      <SessionRail
        sessions={[makeSession({ id: "r1", activity: "inactive", name: "mixed work" })]}
        openTaskCounts={{ r1: { running: 2, ready: 2, blocked: 1 } }}
      />,
    );
    const row = screen.getByText("mixed work").closest('[role="button"]') as HTMLElement;
    const buckets = within(row).getAllByText(/^\d+ (running|ready|blocked)$/);
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

  it("offers no wake control on a sleeping card — the first send wakes it", () => {
    // The ▶ pre-warm is gone from the rail. It only ever bought back the
    // start-up seconds, and it read as a lifecycle state the user had to
    // manage; the kebab in the workspace keeps it for a deliberate warm-up.
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "a1", name: "live one", activity: "active" }),
          makeSession({ id: "r1", name: "resume me", activity: "inactive" }),
        ]}
        openTaskCounts={{}}
      />,
    );
    expect(screen.queryByRole("button", { name: "Resume resume me" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Wake resume me" })).toBeNull();
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

  it("clicking a card only opens it, asleep or awake", () => {
    const onSelect = vi.fn();
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "a1", name: "live one", activity: "active" }),
          makeSession({ id: "r1", name: "resume me", activity: "inactive" }),
        ]}
        openTaskCounts={{}}
        onSelect={onSelect}
      />,
    );
    fireEvent.click(screen.getByText("live one"));
    expect(onSelect).toHaveBeenCalledWith("a1");
    // A sleeping card opens read-only, and wakes on the first send rather
    // than on the click.
    fireEvent.click(screen.getByText("resume me"));
    expect(onSelect).toHaveBeenCalledWith("r1");
  });

  it("pressing Enter on a sleeping row opens it (keyboard-driven)", () => {
    const onSelect = vi.fn();
    render(
      <SessionRail
        sessions={[makeSession({ id: "r1", name: "resume me", activity: "inactive" })]}
        openTaskCounts={{}}
        onSelect={onSelect}
      />,
    );
    const row = screen.getByText("resume me").closest('[role="button"]')!;
    fireEvent.keyDown(row, { key: "Enter" });
    expect(onSelect).toHaveBeenCalledWith("r1");
  });

  it("shows the sleep affordance for active and inactive sessions alike, wired to onSleep", () => {
    // Sleep/terminate is safe against the Supervisor in every activity
    // state, and the activity record can drift from the real child state —
    // so it must never be hidden based on that record alone.
    const onSleep = vi.fn();
    const onSelect = vi.fn();
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "a1", name: "live one", activity: "active" }),
          makeSession({ id: "r1", name: "resume me", activity: "inactive" }),
        ]}
        openTaskCounts={{}}
        onSelect={onSelect}
        onSleep={onSleep}
      />,
    );

    expect(screen.getByRole("button", { name: "Sleep live one" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Sleep resume me" })).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Sleep resume me" }));
    // The whole record, not just the id: the gate that decides whether to
    // ask about unlanded commits lives in useSessionSleep, not here.
    expect(onSleep).toHaveBeenCalledWith(expect.objectContaining({ id: "r1" }));
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("action cluster exposes labeled sleep and archive buttons, grouped under an aria-label", () => {
    render(
      <SessionRail
        sessions={[makeSession({ id: "r1", name: "resume me", activity: "inactive" })]}
        openTaskCounts={{}}
        onSleep={vi.fn()}
      />,
    );
    const cluster = screen.getByRole("group", { name: "resume me actions" });
    expect(within(cluster).getByRole("button", { name: "Sleep resume me" })).toBeTruthy();
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

      const archive = within(cluster).getByRole("button", { name: "Archive anchor me" });
      act(() => archive.focus());
      expect(document.activeElement).toBe(archive);
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
    expect(screen.queryByRole("button", { name: /^Links for/ })).toBeNull();
    // Flag off: no nesting either, so the subject still renders as its own flat row.
    expect(screen.getByText("payments refactor")).toBeTruthy();
  });

  // The relationship is drawn, not captioned: the subject already renders
  // indented underneath its holder, so LEADER / LINKED pills were the caption
  // repeating the indent in caps. Every card, nested or not, has the one
  // chain button that opens the link editor.
  it("nests the subject under its holder with no pill on either row, when the flag is on", () => {
    FLAGS.linkedSessions = true;
    render(<SessionRail sessions={[lead, a]} openTaskCounts={{}} links={[link]} />);
    expect(screen.queryByText("LEADER")).toBeNull();
    expect(screen.queryByText("LINKED")).toBeNull();
    const subject = screen.getByText("payments refactor").closest('[role="button"]')!;
    const trunk = subject.closest('[class*="subjects"]');
    expect(trunk).toBeTruthy();
    // A holder with one subject has nothing to count, so the trunk carries no
    // numeral — "LEADER · 2" only ever earned a count above one.
    expect(trunk?.querySelector('[class*="linkFan"]')).toBeNull();
    expect(screen.getByRole("button", { name: "Links for investigator" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Links for payments refactor" })).toBeTruthy();
  });

  it("keeps a nested row to one control, whatever the orchestrate state", () => {
    FLAGS.linkedSessions = true;
    for (const status of ["pending_restart", "unsupported", "failed", "active"] as const) {
      const { unmount } = render(
        <SessionRail
          sessions={[lead, a]}
          openTaskCounts={{}}
          links={[makeLink({ ...link, orchestrate_status: status })]}
        />,
      );
      expect(screen.queryByText("TOOLS WHEN IDLE")).toBeNull();
      expect(screen.queryByText("TOOLS FAILED")).toBeNull();
      const trigger = screen.getByRole("button", { name: "Links for payments refactor" });
      // Only trouble earns the mark; pending_restart resolves on its own.
      expect(Boolean(trigger.querySelector('[class*="linkBadgeAlert"]'))).toBe(
        status === "unsupported" || status === "failed",
      );
      unmount();
    }
  });

  it("says why the tools failed on the nested row's own chain button", () => {
    FLAGS.linkedSessions = true;
    const failedLink = makeLink({
      ...link,
      orchestrate_status: "failed",
      orchestrate_error: 'exec: "mads": executable file not found in $PATH',
    });
    render(<SessionRail sessions={[lead, a]} openTaskCounts={{}} links={[failedLink]} />);
    const trigger = screen.getByRole("button", { name: "Links for payments refactor" });
    expect(trigger.getAttribute("title")).toMatch(/not found in \$PATH/);
  });

  it("opens the link editor from any card's chain, handing over the button, without opening the session", () => {
    FLAGS.linkedSessions = true;
    const onOpenLinks = vi.fn();
    const onSelect = vi.fn();
    render(
      <SessionRail
        sessions={[lead, a]}
        openTaskCounts={{}}
        links={[link]}
        onSelect={onSelect}
        onOpenLinks={onOpenLinks}
      />,
    );
    const chain = screen.getByRole("button", { name: "Links for payments refactor" });
    fireEvent.click(chain);
    expect(onOpenLinks).toHaveBeenCalledWith("a", chain);
    expect(onSelect).not.toHaveBeenCalled();
  });

  // Mute is shown only in the editor: a rail card states that a link exists,
  // never how loud it is.
  it("never puts the cadence or a mute mark on a card", () => {
    FLAGS.linkedSessions = true;
    const manualLink = makeLink({ ...link, cadence: "manual_only" });
    render(<SessionRail sessions={[lead, a]} openTaskCounts={{}} links={[manualLink]} />);
    const trigger = screen.getByRole("button", { name: "Links for payments refactor" });
    expect(trigger.querySelector("svg")).toBeTruthy();
    expect(trigger.textContent).toBe("1");
    expect(document.querySelector("[data-mute-icon]")).toBeNull();
    expect(screen.queryByText(/Muted|Signals|Follow/)).toBeNull();
  });

  describe("while the link editor is open", () => {
    const b = makeSession({ id: "b", name: "gateway hardening" });
    const editor = (over: Partial<RailLinkEditor> = {}): RailLinkEditor => ({
      focusId: "lead",
      linkedIds: new Set(["a"]),
      blockedReason: () => undefined,
      onPick: vi.fn(() => true),
      ...over,
    });

    it("tags the centred card and checks the ones linked to it", () => {
      FLAGS.linkedSessions = true;
      render(<SessionRail sessions={[lead, a, b]} openTaskCounts={{}} links={[link]} linkEditor={editor()} />);
      const card = (name: string) => screen.getByText(name).closest("[data-rail-card]")!;
      expect(card("investigator").getAttribute("data-link-edit")).toBe("center");
      expect(within(card("investigator") as HTMLElement).getByText("Center")).toBeTruthy();
      expect(card("payments refactor").getAttribute("data-link-edit")).toBe("linked");
      expect(card("gateway hardening").getAttribute("data-link-edit")).toBe("");
    });

    it("turns card, chain and shortcut clicks into picks, and never opens a session", () => {
      FLAGS.linkedSessions = true;
      const watcher = makeSession({ id: "w", name: "watcher" });
      const watch = makeLink({ id: "l7", granting_session_id: "w", subject_session_id: "a", grants: ["read"] });
      const ed = editor();
      const onSelect = vi.fn();
      const onOpenLinks = vi.fn();
      render(
        <SessionRail
          sessions={[lead, a, b, watcher]}
          openTaskCounts={{}}
          links={[link, watch]}
          onSelect={onSelect}
          onOpenLinks={onOpenLinks}
          linkEditor={ed}
        />,
      );
      fireEvent.click(screen.getByText("gateway hardening"));
      expect(ed.onPick).toHaveBeenLastCalledWith("b");
      fireEvent.click(screen.getByRole("button", { name: "Links for investigator" }));
      expect(ed.onPick).toHaveBeenLastCalledWith("lead");
      // Watcher monitors "payments refactor", whose card lives under its leader.
      fireEvent.click(screen.getByRole("button", { name: /^Link payments refactor/ }));
      expect(ed.onPick).toHaveBeenLastCalledWith("a");
      expect(onSelect).not.toHaveBeenCalled();
      expect(onOpenLinks).not.toHaveBeenCalled();
    });

    it("dims a session that can't be linked, and shakes it when it is picked anyway", () => {
      FLAGS.linkedSessions = true;
      const ed = editor({
        blockedReason: (id) => (id === "b" ? "Each has its own isolated copy of the repo." : undefined),
        onPick: vi.fn((id: string) => id !== "b"),
      });
      render(<SessionRail sessions={[lead, a, b]} openTaskCounts={{}} links={[link]} linkEditor={ed} />);
      const card = screen.getByText("gateway hardening").closest("[data-rail-card]")!;
      expect(card.getAttribute("data-link-edit")).toBe("blocked");
      expect(card.getAttribute("aria-disabled")).toBe("true");
      expect(card.getAttribute("title")).toMatch(/isolated copy/);
      expect(within(card as HTMLElement).getByText("separate worktree")).toBeTruthy();
      fireEvent.click(card);
      expect(card.className).toMatch(/nope/);
    });

    it("offers no reorder grip while picking", () => {
      FLAGS.linkedSessions = true;
      render(
        <SessionRail
          sessions={[lead, b]}
          openTaskCounts={{}}
          onMove={() => Promise.resolve()}
          linkEditor={editor({ linkedIds: new Set() })}
        />,
      );
      expect(document.querySelector("[data-rail-grip]")).toBeNull();
    });
  });

  // How the line ENDS is what says which relationship it is. Dashing the line
  // itself was the first attempt and does not survive the geometry: over an
  // elbow this short a dash pattern never establishes a rhythm and renders as
  // a broken box. A solid head drives, an open one reads.
  it("heads a leader's connector differently from a monitor's", () => {
    FLAGS.linkedSessions = true;
    const watched = makeSession({ id: "w", name: "under watch" });
    render(
      <SessionRail
        sessions={[lead, a, watched]}
        openTaskCounts={{}}
        links={[
          link,
          makeLink({ id: "l9", granting_session_id: "lead", subject_session_id: "w", grants: ["read"] }),
        ]}
      />,
    );
    // The head is the last path in the connector: filled for a leader,
    // stroked for a monitor. Asserted on the attributes rather than a class,
    // because that IS the distinction — there is no second element to name.
    const headOf = (name: string) => {
      const paths = screen
        .getByText(name)
        .closest('[role="button"]')!
        .querySelectorAll('[class*="linkConnector"] path');
      return paths[paths.length - 1];
    };
    // Solid: instructions flow into the subject.
    expect(headOf("payments refactor").getAttribute("fill")).toBe("currentColor");
    // Open: a monitor reads its subject, it does not drive it.
    expect(headOf("under watch").getAttribute("fill")).toBeNull();
    expect(headOf("under watch").getAttribute("stroke")).toBe("currentColor");
    // Two subjects now, so the trunk carries the count the pill used to.
    const trunk = screen.getByText("payments refactor").closest('[class*="subjects"]')!;
    expect(trunk.querySelector('[class*="linkFan"]')?.textContent).toBe("2");
  });

  it("runs a holder's link trunk past its own delegates, and gives each subject one box with its delegates", () => {
    FLAGS.linkedSessions = true;
    const dlg = (sessionId: string, id: string) => ({
      id: `dlg_${id}`,
      session_id: sessionId,
      harness_id: id,
      description: `work ${id}`,
      state: "running" as const,
      runs: 1,
      started_at: inFlight(),
      created_at: inFlight(),
      updated_at: inFlight(),
    });
    const state = {
      [lead.id]: { subagents: 1, subagentsSince: inFlight(), delegates: [dlg(lead.id, "h1")] },
      [a.id]: { subagents: 1, subagentsSince: inFlight(), delegates: [dlg(a.id, "s1")] },
    };
    render(<SessionRail sessions={[lead, a]} openTaskCounts={{}} links={[link]} agentStates={state} />);
    const [holderRows, subjectRows] = screen.getAllByTestId("delegate-rows");
    // The holder's delegates sit between it and its subject, so the trunk
    // has to pass them; the subject's own delegates hang below it and don't.
    expect(holderRows.hasAttribute("data-link-trunk")).toBe(true);
    expect(subjectRows.hasAttribute("data-link-trunk")).toBe(false);
    // A lone subject is the last box under the trunk even with delegates
    // after its card — the continuation is drawn per box, so nothing dangles.
    const card = screen.getByText("payments refactor").closest('[role="button"]')!;
    const box = card.parentElement!;
    expect(box.contains(subjectRows)).toBe(true);
    expect(box.parentElement?.lastElementChild).toBe(box);
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

/**
 * A turn that started a minute ago: in flight, and nowhere near the bound past
 * which the rail stops believing a working claim (WORKING_STALE_AFTER_MS). The
 * fixtures below used to carry a fixed 2026 stamp, which read as a turn that
 * had been running for months — exactly the state the rail now refuses to
 * animate.
 */
const inFlight = () => new Date(Date.now() - 60_000).toISOString();

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

  it("renders 'Idle' with a dial for a live session that has said nothing", () => {
    renderWith({});
    const status = statusOf("the session");
    // The mark is an <svg>, so it contributes no text and the line reads as
    // the words alone. That is part of the point: the characters it replaced
    // were in the accessibility tree as well as on screen.
    expect(status.textContent).toBe("Idle");
    expect(status.className).toMatch(/status-idle/);
    expect(status.querySelector('[class*="statusMark"]')).toBeTruthy();
  });

  it("keeps the main agent's clause while only backgrounded subagents run, and counts them in the tooltip", () => {
    // The status line is the one place the fast-changing clause can live, and a
    // backgrounded sidechain can outlast many turns — so it does not get to sit
    // there. It still has to be countable somewhere, which is the tooltip.
    renderWith({
      workingSince: inFlight(),
      label: "Running bash · make check",
      subagents: 2,
      blockingSubagents: 0,
    });
    expect(statusOf("the session").textContent).toContain("Running bash · make check");
    const card = screen.getByText("the session").closest('[role="button"]')!;
    expect(card.getAttribute("title")).toContain("Background subagents: 2 running");
  });

  it("says nothing about background subagents when every running one blocks", () => {
    renderWith({
      workingSince: inFlight(),
      label: "Delegating · review the diff",
      subagents: 2,
      blockingSubagents: 2,
    });
    expect(statusOf("the session").textContent).toContain("2 subagents running");
    const card = screen.getByText("the session").closest('[role="button"]')!;
    expect(card.getAttribute("title") ?? "").not.toContain("Background subagents");
  });

  it("renders the same 'Idle' for an inactive session — the process is not the user's business", () => {
    renderWith({}, makeSession({ id: "s1", name: "the session", activity: "inactive" }));
    expect(statusOf("the session").textContent).toBe("Idle");
  });

  it("renders 'Done · your turn' in the ok tone, as a closed ring", () => {
    renderWith({ done: true });
    const status = statusOf("the session");
    expect(status.textContent).toBe("Done · your turn");
    // One closed circle — not the working mark's track-plus-arc pair, and not
    // the tick, which now belongs to the Set-aside control.
    expect(status.querySelectorAll('[class*="statusMark"] circle').length).toBe(1);
    expect(status.className).toMatch(/status-done/);
    expect(statusOf("the session").querySelector('[class*="dot"]')).toBeNull();
  });

  it("spins while a turn is in flight, and names its subagents", () => {
    renderWith({ workingSince: inFlight(), subagents: 3 });
    const status = statusOf("the session");
    expect(status.textContent).toContain("3 subagents running");
    expect(status.className).toMatch(/status-working/);
    const mark = status.querySelector('[class*="statusMark"]')!;
    // The only animated mark on the rail, and the class that animates it is
    // applied to this kind alone.
    expect(mark.getAttribute("class")).toMatch(/statusMarkSpin/);
    // Decorative: colour and copy carry the state, the motion doesn't.
    expect(mark.getAttribute("aria-hidden")).toBe("true");
  });

  it("renders a parked approval in the warn tone, naming the tool", () => {
    renderWith({ awaitingSince: "2026-07-01T00:00:00Z", awaitingTool: "Bash" });
    const status = statusOf("the session");
    expect(status.textContent).toContain("Needs approval · Bash");
    expect(status.className).toMatch(/status-needs/);
    // A filled triangle, drawn rather than borrowed from the UI font — and
    // deliberately still: `working` is the one state that moves.
    const mark = status.querySelector('[class*="statusMark"]')!;
    expect(mark.querySelector("path")!.getAttribute("fill")).toBe("currentColor");
    expect(mark.getAttribute("class")).not.toMatch(/statusMarkSpin/);
  });

  it("renders a crash in the bad tone, counting the blocked work it left behind", () => {
    renderWith({ crashed: true }, live, { s1: { running: 0, ready: 0, blocked: 2 } });
    const status = statusOf("the session");
    expect(status.textContent).toBe("Crashed · 2 blocked tasks");
    expect(status.className).toMatch(/status-bad/);
  });

  it("draws no status dot on any card", () => {
    const { unmount } = render(
      <SessionRail
        sessions={[live]}
        openTaskCounts={{}}
        agentStates={{ s1: { subagents: 0, crashed: true } }}
      />,
    );
    expect(screen.getByText("the session").closest('[role="button"]')!.querySelector('[class*="dot"]')).toBeNull();
    unmount();

    renderWith({ workingSince: inFlight() });
    expect(screen.getByText("the session").closest('[role="button"]')!.querySelector('[class*="dot"]')).toBeNull();
  });

  it("holds the documented precedence — crash over approval over working over delegating over done", () => {
    const everything: SessionAgentState = {
      crashed: true,
      awaitingSince: "2026-07-01T00:00:00Z",
      awaitingTool: "Bash",
      workingSince: inFlight(),
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
    // The turn is over but its delegates aren't: still busy, and the unseen
    // turn end is still news.
    expect(statusOf("the session").textContent).toBe("Done · 2 subagents running");

    rerender(
      <SessionRail
        sessions={[live]}
        openTaskCounts={{}}
        agentStates={{
          s1: { crashed: false, subagents: 0, done: true },
        }}
      />,
    );
    expect(statusOf("the session").textContent).toBe("Done · your turn");
  });

  it("reads a session whose only activity is a running delegate as busy, not Idle", () => {
    render(
      <SessionRail
        sessions={[live]}
        openTaskCounts={{}}
        agentStates={{ s1: { subagents: 1, blockingSubagents: 0, subagentsSince: inFlight() } }}
      />,
    );
    expect(statusOf("the session").textContent).toMatch(/^1 subagent running/);
  });

  it("nests a session's delegates under its card, opens one on click, and hides them when the setting is off", () => {
    const running = {
      id: "dlg_1",
      session_id: "s1",
      harness_id: "a1",
      description: "Audit error handling",
      state: "running" as const,
      runs: 1,
      started_at: inFlight(),
      created_at: inFlight(),
      updated_at: inFlight(),
    };
    const onOpenDelegate = vi.fn();
    const state = { s1: { subagents: 1, subagentsSince: inFlight(), delegates: [running] } };
    const { rerender } = render(
      <SessionRail sessions={[live]} openTaskCounts={{}} agentStates={state} onOpenDelegate={onOpenDelegate} />,
    );
    const rows = screen.getByTestId("delegate-rows");
    expect(rows.textContent).toContain("Audit error handling");
    fireEvent.click(within(rows).getByRole("button"));
    expect(onOpenDelegate).toHaveBeenCalledWith(expect.objectContaining({ harness_id: "a1" }));

    rerender(
      <SessionRail sessions={[live]} openTaskCounts={{}} agentStates={state} showDelegates={false} />,
    );
    expect(screen.queryByTestId("delegate-rows")).toBeNull();
    // The status line still counts them: the setting hides rows, not facts.
    expect(statusOf("the session").textContent).toMatch(/1 subagent running/);
  });

  it("renders the same line shape in every state, so card height can't change with it", () => {
    const states: SessionAgentState[] = [
      { subagents: 0 },
      { subagents: 0, done: true },
      { subagents: 0, workingSince: inFlight() },
      { subagents: 3, workingSince: inFlight() },
      { subagents: 2, subagentsSince: inFlight() },
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
          c: { subagents: 1, workingSince: inFlight() },
          a: { subagents: 0, done: true },
        }}
      />,
    );
    expect(order()).toEqual(["alpha", "bravo", "charlie"]);
  });

  // The 2026-10-01 rail: two cards spinning "Thinking… 23h", one of them for a
  // session whose provider child had been gone for most of that time. Nothing
  // but the Runner that wrote agent_state ever retires it, so a wedged agent's
  // last word stands forever — the card must stop asserting it in the present
  // tense, and must stop animating while it does.
  it("stops spinning, and says so, once a working claim has outlived any real turn", () => {
    renderWith({
      workingSince: new Date(Date.now() - 23 * 60 * 60 * 1000).toISOString(),
      label: "Thinking…",
    });
    const status = statusOf("the session");
    expect(status.textContent).toBe("Stalled · no agent activity23h");
    expect(status.className).toMatch(/status-needs/);
    // The working spinner's own geometry, held still — which is exactly the
    // claim the line makes. The one state that animates is the one moving.
    expect(status.querySelector('[class*="statusMarkSpin"]')).toBeNull();
    expect(status.querySelectorAll('[class*="statusMark"] circle').length).toBe(2);
    // The clause it last reported is still the most useful thing to know — it
    // just has to come with the time attached rather than as present tense.
    const card = screen.getByText("the session").closest('[role="button"]')!;
    expect(card.getAttribute("title")).toContain("Last reported: Thinking…");
  });

  it("counts a live state's elapsed time off the shared 1s tick", () => {
    vi.useFakeTimers({ now: Date.parse("2026-07-01T00:00:04Z") });
    try {
      // Stamped against the fake clock, not the real one: inFlight() would be
      // decades ahead of it and read as a turn that hasn't started.
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

  it("spins exactly one mark, and only the working one", () => {
    const sheet = css();
    expect(sheet.match(/\.statusMarkSpin \.statusMarkArc \{([^}]*)\}/)![1]).toMatch(
      /animation:\s*railSpin/,
    );
    // No per-mark reduced-motion override: styles/global.css stops every
    // animation wholesale, and stopped, the arc simply rests. The CSS-border
    // spinner this replaced did need one — frozen, it showed a single accent
    // side against three --line ones, a shape that meant nothing.
    expect(sheet).not.toContain("statusSpinner");
    expect(sheet.match(/animation:\s*railSpin/g)!.length).toBe(1);
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

    await waitFor(() => expect(statusOf("session 7").textContent).toBe("Done · your turn"));
    expect(statusOf("session 3").textContent).toBe("Idle");
    expect(FakeEventSource.instances.filter((i) => !i.closed)).toHaveLength(1);
  });

  it("paints the server-built clause a live turn reports, not a generic 'Working…'", async () => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    const sessions = [makeSession({ id: "s1", name: "live one" })];
    render(<Harness sessions={sessions} />);

    const turnStart = inFlight();
    emitAgentState("s1", { state: "working", label: "Working…", since: turnStart });
    // The elapsed counter rides the real clock here, so only the clause is read.
    await waitFor(() => expect(clauseOf("live one")).toBe("Working…"));

    // The same string the open conversation's working-indicator ticker shows
    // for this call: one server-side enrichment feeds both surfaces.
    emitAgentState(
      "s1",
      { state: "working", label: "Running bash · make check", since: turnStart },
      2,
    );
    await waitFor(() => expect(clauseOf("live one")).toBe("Running bash · make check"));
    emitAgentState("s1", { state: "working", label: "Thinking…", since: turnStart }, 3);
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
    await waitFor(() => expect(statusOf("killed one").textContent).toBe("Idle"));
  });
});

/** The rail's rendered card order, top to bottom. */
function cardNames(): string[] {
  return [...screen.getByLabelText("Session list").querySelectorAll('div[role="button"]')].map(
    (c) => c.querySelector('[class*="name"]')!.textContent!,
  );
}

/** Card names in one of the rail's two lists — the active one, or Done. */
function listNames(list: "active" | "done"): string[] {
  const el = document.querySelector(`[data-rail-list="${list}"]`);
  if (!el) return [];
  return [...el.querySelectorAll('div[role="button"]')].map(
    (c) => c.querySelector('[class*="name"]')!.textContent!,
  );
}

const activeNames = () => listNames("active");
const doneNames = () => listNames("done");

function chip(label: string): HTMLElement {
  const bar = screen.getByRole("group", { name: "Filter sessions" });
  return [...bar.querySelectorAll("button")].find((b) => b.textContent!.startsWith(label))!;
}

describe("SessionRail filter chips", () => {
  // One of each bucket, newest start first — the active order is start time,
  // so these created_at values are what lay the list out.
  const working = makeSession({ id: "w", name: "working one", created_at: isoAgo(1 * MINUTE) });
  const parked = makeSession({ id: "p", name: "parked one", created_at: isoAgo(2 * MINUTE) });
  const crashed = makeSession({ id: "x", name: "crashed one", created_at: isoAgo(3 * MINUTE) });
  const ready = makeSession({ id: "r", name: "ready one", created_at: isoAgo(4 * MINUTE) });
  const sleeping = makeSession({
    id: "s",
    name: "sleeping one",
    activity: "inactive",
    created_at: isoAgo(5 * MINUTE),
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
    expect(chip("Attn.").textContent).toBe("Attn.2");
    expect(chip("Attn.").title).toBe("Needs you");
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
    expect(chip("Attn.").textContent).toBe("Attn.1");
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

    fireEvent.click(chip("Attn."));
    expect(cardNames()).toEqual(["parked one", "crashed one"]);
    expect(chip("Attn.").getAttribute("aria-pressed")).toBe("true");

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

describe("SessionRail — no time-based fold", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  // The rail used to fold everything a day cold behind "Earlier". Age is not
  // a judgement, so it no longer hides anything: only the user's ✓ does.
  it("keeps a session whose last reply is weeks old in the active list, with no toggle", () => {
    vi.useFakeTimers({ now: NOW });
    const fresh = makeSession({
      id: "f",
      name: "fresh one",
      created_at: isoAgo(10 * MINUTE),
      last_turn_at: isoAgo(5 * MINUTE),
    });
    const ancient = makeSession({
      id: "l",
      name: "ancient one",
      created_at: isoAgo(30 * DAY),
      last_turn_at: isoAgo(21 * DAY),
    });
    render(<SessionRail sessions={[fresh, ancient]} openTaskCounts={{}} />);

    expect(activeNames()).toEqual(["fresh one", "ancient one"]);
    expect(screen.queryByRole("button", { expanded: false })).toBeNull();
    expect(screen.queryByRole("button", { name: /Earlier|Done/ })).toBeNull();
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

describe("SessionRail — Done", () => {
  // The user's own judgement about a session, which is the one piece of rail
  // state no amount of watching the session could derive.
  const kept = makeSession({ id: "k", name: "keep me", last_turn_at: isoAgo(5 * MINUTE) });
  const finished = makeSession({
    id: "a",
    name: "done with this",
    last_turn_at: isoAgo(5 * MINUTE),
    tags: ["aside"],
  });

  function doneToggle(): HTMLElement {
    return screen.getByRole("button", { name: /^Done/ });
  }

  // The open state is remembered, so one test's collapse must not leak into
  // the next one's "open by default".
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("puts a checked-off session under an open 'Done · 1' toggle, below the active list", () => {
    vi.useFakeTimers({ now: NOW });
    render(<SessionRail sessions={[kept, finished]} openTaskCounts={{}} />);

    expect(activeNames()).toEqual(["keep me"]);
    expect(doneNames()).toEqual(["done with this"]);
    expect(doneToggle().textContent).toContain("· 1");
    expect(doneToggle().getAttribute("aria-expanded")).toBe("true");
    // Below, not beside: the whole rail reads active first, then Done.
    expect(cardNames()).toEqual(["keep me", "done with this"]);
  });

  it("collapses on the toggle, and remembers that across a remount", () => {
    vi.useFakeTimers({ now: NOW });
    const { unmount } = render(<SessionRail sessions={[kept, finished]} openTaskCounts={{}} />);

    fireEvent.click(doneToggle());
    expect(doneToggle().getAttribute("aria-expanded")).toBe("false");
    expect(cardNames()).toEqual(["keep me"]);
    expect(localStorage.getItem(DONE_OPEN_KEY)).toBe("0");

    unmount();
    render(<SessionRail sessions={[kept, finished]} openTaskCounts={{}} />);
    expect(doneToggle().getAttribute("aria-expanded")).toBe("false");
    expect(cardNames()).toEqual(["keep me"]);
  });

  it("keeps a done session in the active list while it needs the user", () => {
    // "Done until it needs me". A check-off that could swallow a parked
    // approval would stop being trusted, and an untrusted one is worse than
    // none at all.
    vi.useFakeTimers({ now: NOW });
    render(
      <SessionRail
        sessions={[kept, finished]}
        openTaskCounts={{}}
        agentStates={{ a: { subagents: 0, awaitingSince: isoAgo(MINUTE) } }}
      />,
    );

    // At its own rank (equal start times, so id order), not hoisted to the top.
    expect(activeNames()).toEqual(["done with this", "keep me"]);
    expect(screen.queryByRole("button", { name: /^Done/ })).toBeNull();
  });

  it("returns it to Done once the attention is resolved, without clearing the tag", () => {
    vi.useFakeTimers({ now: NOW });
    const { rerender } = render(
      <SessionRail
        sessions={[kept, finished]}
        openTaskCounts={{}}
        agentStates={{ a: { subagents: 0, awaitingSince: isoAgo(MINUTE) } }}
      />,
    );
    expect(activeNames()).toContain("done with this");

    rerender(<SessionRail sessions={[kept, finished]} openTaskCounts={{}} agentStates={{}} />);
    expect(activeNames()).toEqual(["keep me"]);
    expect(doneNames()).toEqual(["done with this"]);
  });

  it("shows every done card in one list while a query is live — going looking is the point", () => {
    vi.useFakeTimers({ now: NOW });
    render(<SessionRail sessions={[kept, finished]} openTaskCounts={{}} />);

    fireEvent.change(screen.getByPlaceholderText(/Search sessions/), {
      target: { value: "done" },
    });
    expect(activeNames()).toEqual(["done with this"]);
    expect(screen.queryByRole("button", { name: /^Done/ })).toBeNull();
  });

  it("checks off through onSetDone, and never opens the card", () => {
    vi.useFakeTimers({ now: NOW });
    const onSetDone = vi.fn();
    const onSelect = vi.fn();
    render(
      <SessionRail sessions={[kept]} openTaskCounts={{}} onSetDone={onSetDone} onSelect={onSelect} />,
    );

    const button = screen.getByRole("button", { name: "Check off keep me" });
    expect(button.getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(button);
    expect(onSetDone).toHaveBeenCalledWith(kept, true, false);
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("tells the owner the session is mid-turn, so it can ask before stopping it", () => {
    vi.useFakeTimers({ now: NOW });
    const onSetDone = vi.fn();
    render(
      <SessionRail
        sessions={[kept]}
        openTaskCounts={{}}
        agentStates={{ k: { subagents: 0, workingSince: isoAgo(30_000) } }}
        onSetDone={onSetDone}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Check off keep me" }));
    expect(onSetDone).toHaveBeenCalledWith(kept, true, true);
  });

  it("offers the reverse on a done card", () => {
    vi.useFakeTimers({ now: NOW });
    const onSetDone = vi.fn();
    render(<SessionRail sessions={[finished]} openTaskCounts={{}} onSetDone={onSetDone} />);

    const button = screen.getByRole("button", { name: "Bring back done with this" });
    expect(button.getAttribute("aria-pressed")).toBe("true");

    fireEvent.click(button);
    expect(onSetDone).toHaveBeenCalledWith(finished, false, false);
  });

  it("offers no control at all when the surface passes no handler", () => {
    vi.useFakeTimers({ now: NOW });
    render(<SessionRail sessions={[kept]} openTaskCounts={{}} />);
    expect(screen.queryByRole("button", { name: "Check off keep me" })).toBeNull();
  });

  // Finished most recently first: last reply stands in for "finished at", so
  // a session started long ago but wrapped up today is the one on top.
  it("orders Done by last reply, newest first — not by when the session started", () => {
    vi.useFakeTimers({ now: NOW });
    const doneAt = (id: string, name: string, started: number, replied: number) =>
      makeSession({
        id,
        name,
        created_at: isoAgo(started),
        last_turn_at: isoAgo(replied),
        tags: ["aside"],
      });
    render(
      <SessionRail
        sessions={[
          doneAt("d1", "started last", 1 * HOUR, 3 * HOUR),
          doneAt("d2", "started first", 9 * DAY, 10 * MINUTE),
          doneAt("d3", "in between", 2 * DAY, 1 * HOUR),
        ]}
        openTaskCounts={{}}
      />,
    );
    expect(doneNames()).toEqual(["started first", "in between", "started last"]);
  });

  it("caps Done at five rows behind 'Show N more', which expands and reads 'Show fewer'", () => {
    vi.useFakeTimers({ now: NOW });
    const seven = Array.from({ length: 7 }, (_, i) =>
      makeSession({
        id: `d${i}`,
        name: `done ${i}`,
        last_turn_at: isoAgo((i + 1) * HOUR),
        tags: ["aside"],
      }),
    );
    render(<SessionRail sessions={seven} openTaskCounts={{}} />);

    expect(doneToggle().textContent).toContain("· 7");
    expect(doneNames()).toEqual(["done 0", "done 1", "done 2", "done 3", "done 4"]);

    fireEvent.click(screen.getByRole("button", { name: "Show 2 more" }));
    expect(doneNames()).toEqual(seven.map((s) => s.name));

    fireEvent.click(screen.getByRole("button", { name: "Show fewer" }));
    expect(doneNames()).toHaveLength(5);
  });
});

describe("SessionRail omni search", () => {
  // The mockup's own fixture set. "rail readability pass" is the case that
  // decides the field list: its workspace is "Main work", which shares no
  // substring with ~/repo/mads-ui, so both have to be indexed. Listed newest
  // start first, which is the resting order.
  const goose = makeSession({
    id: "g",
    name: "goose ACP transcript gap",
    workspace: "mads",
    cwd: "~/repo/mads",
    worktree_branch: "wt/goose-acp",
    created_at: isoAgo(1 * MINUTE),
  });
  const electron = makeSession({
    id: "e",
    name: "electron self-heal reinstall",
    workspace: "mads",
    cwd: "~/repo/mads",
    created_at: isoAgo(6 * MINUTE),
  });
  const retro = makeSession({
    id: "s",
    name: "retro: conversation UX",
    workspace: "sidecar",
    cwd: "~/repo/sidecar",
    created_at: isoAgo(3 * HOUR),
  });
  const railUi = makeSession({
    id: "u",
    name: "rail readability pass",
    workspace: "Main work",
    cwd: "~/repo/mads-ui",
    created_at: isoAgo(5 * HOUR),
  });
  const digest = makeSession({
    id: "d",
    name: "digest watcher backlog",
    workspace: "sidecar",
    cwd: "~/repo/sidecar",
    worktree_branch: "wt/digest",
    created_at: isoAgo(2 * DAY),
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
    // A test here collapses Done, and that choice is persisted.
    localStorage.clear();
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

  it("reaches sessions in a collapsed Done list", () => {
    const shelved = { ...digest, tags: ["aside"] };
    renderRail({ sessions: [goose, electron, retro, railUi, shelved] });
    fireEvent.click(screen.getByRole("button", { name: /^Done/ }));
    // At rest the checked-off card is behind the collapsed toggle.
    expect(cardNames()).not.toContain("digest watcher backlog");

    type("digest");
    expect(cardNames()).toEqual(["digest watcher backlog"]);
    // Search bypasses the view: no Done section is drawn over a result list.
    expect(screen.queryByRole("button", { name: /^Done/ })).toBeNull();
  });

  it("orders hits by relevance and restores the resting order identically on clear", () => {
    renderRail();
    const resting = cardNames();
    expect(resting).toEqual([
      "goose ACP transcript gap",
      "electron self-heal reinstall",
      "retro: conversation UX",
      "rail readability pass",
      "digest watcher backlog",
    ]);

    // "re" is a name prefix for "retro", a name substring for the other two
    // "…re…" names, and only a ~/repo hit for goose and digest — which
    // relevance drops to the bottom, in their resting order.
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

describe("SessionRail — recursive link tree", () => {
  const LEAD: Link["grants"] = ["read", "inject", "orchestrate"];
  const WATCH: Link["grants"] = ["read"];
  // Newest start first is the active order, so these created_at values lay
  // the roots out top to bottom.
  const at = (n: number) => `2026-07-0${n}T00:00:00Z`;
  const captain = makeSession({ id: "captain", name: "Captain", created_at: at(9) });
  const deploy = makeSession({ id: "deploy", name: "Deploy", created_at: at(8) });
  const railS = makeSession({ id: "rail", name: "Rail", created_at: at(7) });
  const merge = makeSession({ id: "merge", name: "Merge", created_at: at(6) });
  const chain = [
    makeLink({ id: "l1", granting_session_id: "captain", subject_session_id: "deploy", grants: LEAD }),
    makeLink({ id: "l2", granting_session_id: "deploy", subject_session_id: "rail", grants: LEAD }),
    makeLink({ id: "l3", granting_session_id: "deploy", subject_session_id: "merge", grants: LEAD }),
  ];
  const watcher = makeSession({ id: "watcher", name: "Watcher", created_at: at(5) });
  const watchRail = makeLink({
    id: "m1",
    granting_session_id: "watcher",
    subject_session_id: "rail",
    grants: WATCH,
  });

  const shortcut = (name: string) =>
    screen.getByRole("button", { name: new RegExp(`^Jump to ${name}`) });

  beforeEach(() => {
    FLAGS.linkedSessions = true;
  });
  afterEach(() => {
    FLAGS.linkedSessions = false;
    vi.useRealTimers();
  });

  it("renders every session of a 3-level chain, each nested one level under its leader", () => {
    render(<SessionRail sessions={[captain, deploy, railS, merge]} openTaskCounts={{}} links={chain} />);
    expect(cardNames()).toEqual(["Captain", "Deploy", "Rail", "Merge"]);
    const card = (name: string) => screen.getByText(name).closest('[role="button"]')!;
    // Rail hangs off Deploy's own trunk, which hangs off Captain's.
    const deployBox = card("Deploy").parentElement!;
    expect(deployBox.contains(card("Rail"))).toBe(true);
    expect(deployBox.contains(card("Merge"))).toBe(true);
    expect(card("Captain").parentElement!.contains(deployBox)).toBe(true);
    // Deploy's trunk counts its own two subjects.
    expect(deployBox.querySelector('[class*="linkFan"]')?.textContent).toBe("2");
  });

  it("draws a subject with a leader and a monitor once, under the leader, and a shortcut under the monitor", () => {
    render(
      <SessionRail
        sessions={[captain, deploy, railS, merge, watcher]}
        openTaskCounts={{}}
        links={[...chain, watchRail]}
      />,
    );
    expect(cardNames()).toEqual(["Captain", "Deploy", "Rail", "Merge", "Watcher"]);
    expect(screen.getAllByText("Rail")).toHaveLength(2);
    const sc = shortcut("Rail");
    expect(sc.title).toBe("Shown under Deploy. Click to jump.");
    expect(sc.textContent).toBe("◉Rail↗");
    // Under the watcher, not a card of its own and not in the card walk.
    const watcherCard = screen.getByText("Watcher").closest('[role="button"]')!;
    expect(watcherCard.nextElementSibling?.contains(sc)).toBe(true);
    expect(sc.hasAttribute("data-rail-card")).toBe(false);
  });

  it("homes a subject with two monitors under the earliest link", () => {
    const m2 = makeSession({ id: "m2", name: "Second", created_at: at(4) });
    const s = makeSession({ id: "s", name: "Subject", created_at: at(3) });
    render(
      <SessionRail
        sessions={[watcher, m2, s]}
        openTaskCounts={{}}
        links={[
          makeLink({ id: "a", granting_session_id: "watcher", subject_session_id: "s", grants: WATCH, created_at: at(5) }),
          makeLink({ id: "b", granting_session_id: "m2", subject_session_id: "s", grants: WATCH, created_at: at(2) }),
        ]}
      />,
    );
    expect(cardNames()).toEqual(["Watcher", "Second", "Subject"]);
    const secondCard = screen.getByText("Second").closest('[role="button"]')!;
    expect(secondCard.parentElement!.querySelector('[class*="subjects"]')).toBeTruthy();
    expect(shortcut("Subject").title).toBe("Shown under Second. Click to jump.");
  });

  it("drops no session from a monitor cycle", () => {
    const a = makeSession({ id: "a", name: "Alpha", created_at: at(3) });
    const b = makeSession({ id: "b", name: "Bravo", created_at: at(2) });
    const c = makeSession({ id: "c", name: "Charlie", created_at: at(1) });
    render(
      <SessionRail
        sessions={[a, b, c]}
        openTaskCounts={{}}
        links={[
          makeLink({ id: "ab", granting_session_id: "a", subject_session_id: "b", grants: WATCH }),
          makeLink({ id: "bc", granting_session_id: "b", subject_session_id: "c", grants: WATCH }),
          makeLink({ id: "ca", granting_session_id: "c", subject_session_id: "a", grants: WATCH }),
        ]}
      />,
    );
    expect(cardNames()).toEqual(["Alpha", "Bravo", "Charlie"]);
    // Alpha is first in rail order, so the loop is cut there and closes with a shortcut.
    expect(shortcut("Alpha").title).toBe("Shown at the top level. Click to jump.");
  });

  it("makes a subject a root when its holder isn't in the rail's list", () => {
    // Captain is outside this rail's sessions (another workspace, say).
    render(<SessionRail sessions={[deploy, railS, merge]} openTaskCounts={{}} links={chain} />);
    expect(cardNames()).toEqual(["Deploy", "Rail", "Merge"]);
    const deployCard = screen.getByText("Deploy").closest('[role="button"]')!;
    expect(deployCard.querySelector('[class*="linkConnector"]')).toBeNull();
  });

  it("jumps to the real card from a shortcut: scrolls it into view and flashes it", () => {
    const scroll = vi.fn();
    const original = Element.prototype.scrollIntoView;
    Element.prototype.scrollIntoView = scroll;
    try {
      render(
        <SessionRail
          sessions={[captain, deploy, railS, merge, watcher]}
          openTaskCounts={{}}
          links={[...chain, watchRail]}
          onSelect={vi.fn()}
        />,
      );
      fireEvent.click(shortcut("Rail"));
      const card = document.querySelector<HTMLElement>('[data-rail-card][data-rail-id="rail"]')!;
      expect(scroll).toHaveBeenCalledTimes(1);
      expect(scroll.mock.contexts[0]).toBe(card);
      expect(scroll).toHaveBeenCalledWith({ block: "nearest" });
      expect(card.className).toMatch(/flash/);
      fireEvent.animationEnd(card);
      expect(card.className).not.toMatch(/flash/);
    } finally {
      Element.prototype.scrollIntoView = original;
    }
  });

  it("rolls a grandchild's urgency up to its root, and never a shortcut's", () => {
    vi.useFakeTimers({ now: NOW });
    render(
      <SessionRail
        sessions={[captain, deploy, railS, merge, watcher]}
        openTaskCounts={{}}
        links={[...chain, watchRail]}
        // Rail (a grandchild of Captain, a shortcut under Watcher) is parked.
        agentStates={{ rail: { subagents: 0, awaitingSince: isoAgo(MINUTE), awaitingTool: "Bash" } }}
      />,
    );
    // One group needs the user: Captain's. Watcher only points at Rail.
    expect(chip("Attn.").textContent).toBe("Attn.1");
    expect(chip("Idle").textContent).toBe("Idle1");
    fireEvent.click(chip("Attn."));
    expect(cardNames()).toEqual(["Captain", "Deploy", "Rail", "Merge"]);
    fireEvent.click(chip("Idle"));
    expect(cardNames()).toEqual(["Watcher"]);
  });

  it("checks a root off with every session under it, and pulls it back for a parked grandchild only", () => {
    vi.useFakeTimers({ now: NOW });
    const doneCaptain = { ...captain, tags: ["aside"] };
    const doneWatcher = { ...watcher, tags: ["aside"] };
    const { rerender } = render(
      <SessionRail
        sessions={[doneCaptain, deploy, railS, merge, doneWatcher]}
        openTaskCounts={{}}
        links={[...chain, watchRail]}
      />,
    );
    expect(activeNames()).toEqual([]);
    expect(doneNames()).toEqual(expect.arrayContaining(["Captain", "Deploy", "Rail", "Merge", "Watcher"]));

    rerender(
      <SessionRail
        sessions={[doneCaptain, deploy, railS, merge, doneWatcher]}
        openTaskCounts={{}}
        links={[...chain, watchRail]}
        agentStates={{ rail: { subagents: 0, awaitingSince: isoAgo(MINUTE), awaitingTool: "Bash" } }}
      />,
    );
    // The parked grandchild brings its whole group back; the watcher, which
    // only holds a shortcut to it, stays checked off.
    expect(activeNames()).toEqual(["Captain", "Deploy", "Rail", "Merge"]);
    expect(doneNames()).toEqual(["Watcher"]);
  });

  it("puts the same chain glyph on every card, tinted and counted once it has links", () => {
    render(
      <SessionRail
        sessions={[captain, deploy, railS, merge, watcher, makeSession({ id: "solo", name: "Solo" })]}
        openTaskCounts={{}}
        links={[...chain, watchRail]}
      />,
    );
    const countOf = (label: string) => {
      const button = screen.getByRole("button", { name: label });
      expect(button.querySelector("svg path")?.getAttribute("d")).toBe(
        "M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71",
      );
      return {
        count: button.querySelector("[data-link-count]")?.textContent,
        tinted: /linkTriggerLinked/.test(button.className),
      };
    };
    expect(countOf("Links for Captain")).toEqual({ count: "1", tinted: true });
    // Deploy: led by Captain, leads Rail and Merge.
    expect(countOf("Links for Deploy")).toEqual({ count: "3", tinted: true });
    // Rail: led by Deploy, watched by Watcher.
    expect(countOf("Links for Rail")).toEqual({ count: "2", tinted: true });
    expect(countOf("Links for Watcher")).toEqual({ count: "1", tinted: true });
    expect(countOf("Links for Solo")).toEqual({ count: undefined, tinted: false });
  });
});
