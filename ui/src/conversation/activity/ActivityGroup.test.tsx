import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { ActivityGroup, placePeek } from "./ActivityGroup";
import type { PermissionRequest } from "../../router/types";
import type { ActivityEntry } from "./conversationItems";

const entries: ActivityEntry[] = [
  {
    type: "tool",
    tool: {
      id: "t1",
      name: "Grep",
      kind: "search",
      title: "recordFileChange in services/session",
      input: { pattern: "recordFileChange", path: "services/session" },
      result: { text: "runner.go:412: func recordFileChange", timestamp: "2026-09-09T10:00:01Z" },
    },
    timestamp: "2026-09-09T10:00:00Z",
  },
  {
    type: "tool",
    tool: {
      id: "t2",
      name: "Read",
      kind: "read",
      title: "first-touch classification path",
      locations: [{ path: "services/session/internal/runner/runner.go" }],
      result: { text: "touched := map[string]bool{}", timestamp: "2026-09-09T10:00:02Z" },
    },
    timestamp: "2026-09-09T10:00:01Z",
  },
];

describe("ActivityGroup", () => {
  it("collapsed by default: one-line header with count, kind set, duration and aggregate status", () => {
    render(<ActivityGroup entries={entries} />);
    const header = screen.getByRole("button", { name: /2 actions · 1 search · 1 read/ });
    expect(header.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryAllByTestId("activity-row")).toHaveLength(0);
    // duration spans first start → last result
    expect(header.textContent).toContain("2.0s");
    expect(header.querySelector('[data-status="completed"]')).toBeTruthy();
  });

  it("shows no duration while any call in the group is unresolved (a partial span would report only the first tool's duration)", () => {
    const mixed: ActivityEntry[] = [
      entries[0], // resolved, 1s
      {
        type: "tool",
        // live arrival with no result yet — no stamps to span honestly
        tool: { id: "t9", name: "Bash", kind: "execute", title: "make check" },
      },
    ];
    render(<ActivityGroup entries={mixed} />);
    const header = screen.getByRole("button", { name: /2 actions/ });
    expect(header.textContent).not.toContain("1.0s");
    expect(header.textContent).not.toMatch(/\d+ms/);
  });

  it("portals the peek out of the row, so no scroll container can clip it", () => {
    render(<ActivityGroup entries={entries} defaultOpen />);
    const row = screen.getAllByTestId("activity-row")[0];
    fireEvent.mouseEnter(row);
    const peek = screen.getByTestId("activity-peek");
    expect(row.contains(peek)).toBe(false);
    expect(peek.parentElement).toBe(document.body);
  });

  it("places the peek below the row when it fits, above when only that fits", () => {
    // 1000px viewport, 200px card.
    expect(placePeek({ top: 100, bottom: 130 }, 200, 1000)).toEqual({ top: 132, up: false });
    expect(placePeek({ top: 900, bottom: 930 }, 200, 1000)).toEqual({ top: 698, up: true });
    // The reported case: a row near the top of the subagent dock used to flip
    // up against the dock body's bottom edge and get cut off at its top. Only
    // the viewport bounds it now, so it opens downward.
    expect(placePeek({ top: 40, bottom: 70 }, 200, 1000)).toEqual({ top: 72, up: false });
  });

  it("clamps a peek that fits on neither side into the viewport, on the roomier side", () => {
    // 400px viewport, 300px card: no room above or below a mid-low row.
    expect(placePeek({ top: 250, bottom: 280 }, 300, 400)).toEqual({ top: 8, up: true });
    expect(placePeek({ top: 90, bottom: 120 }, 300, 400)).toEqual({ top: 92, up: false });
  });

  it("prefixes unknown-kind rows with the tool name (fallback titles are bare input previews)", () => {
    const unknown: ActivityEntry[] = [
      {
        type: "tool",
        tool: { id: "t1", name: "ScheduleWakeup", input: { stop: true } },
      },
    ];
    render(<ActivityGroup entries={unknown} defaultOpen />);
    expect(screen.getByText("ScheduleWakeup")).toBeTruthy();
  });

  it("expanding shows one row per call with server-built titles and location basenames", () => {
    render(<ActivityGroup entries={entries} />);
    fireEvent.click(screen.getByRole("button", { name: /2 actions/ }));
    const rows = screen.getAllByTestId("activity-row");
    expect(rows).toHaveLength(2);
    expect(screen.getByText("recordFileChange in services/session")).toBeTruthy();
    // location basename, dim, next to the title
    expect(screen.getByText("runner.go")).toBeTruthy();
  });

  it("a live group renders open, and collapses when the turn ends (live → false)", () => {
    const liveEntries = entries.map((e) => ({ ...e, live: true }));
    const { rerender } = render(<ActivityGroup entries={liveEntries} live />);
    expect(screen.getAllByTestId("activity-row")).toHaveLength(2);

    rerender(<ActivityGroup entries={entries} live={false} />);
    expect(screen.queryAllByTestId("activity-row")).toHaveLength(0);
  });

  it("a live group's header spins only while a call in it is unresolved", () => {
    const liveEntries = entries.map((e) => ({ ...e, live: true }));
    const { rerender } = render(<ActivityGroup entries={liveEntries} live />);
    const header = screen.getByRole("button", { name: /2 actions/ });
    expect(header.querySelector('[data-status="in_progress"]')).toBeNull();
    expect(header.querySelector('[data-status="completed"]')).toBeTruthy();

    const pending: ActivityEntry = {
      type: "tool",
      tool: { id: "t9", name: "Bash", kind: "execute", title: "make check" },
      live: true,
    };
    rerender(<ActivityGroup entries={[...liveEntries, pending]} live />);
    expect(screen.getByRole("button", { name: /3 actions/ }).querySelector('[data-status="in_progress"]')).toBeTruthy();
  });

  it("row click opens the dock target; rows are inert without onOpenTool", () => {
    const onOpen = vi.fn();
    const { unmount } = render(<ActivityGroup entries={entries} defaultOpen onOpenTool={onOpen} />);
    fireEvent.click(screen.getByText("recordFileChange in services/session"));
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ id: "t1" }));
    unmount();

    render(<ActivityGroup entries={entries} defaultOpen />);
    const row = screen.getAllByTestId("activity-row")[0];
    expect(row.querySelector("button")).toBeNull();
  });

  it("hover shows a capped input/result peek; suppressed while that row's detail is docked", () => {
    render(<ActivityGroup entries={entries} defaultOpen selectedToolId="t1" onOpenTool={() => {}} />);
    const [grepRow, readRow] = screen.getAllByTestId("activity-row");

    // t1 is open in the dock — its peek must NOT appear on hover.
    fireEvent.mouseEnter(grepRow);
    expect(screen.queryByTestId("activity-peek")).toBeNull();
    fireEvent.mouseLeave(grepRow);

    // Any other row peeks normally, with input and result previews.
    fireEvent.mouseEnter(readRow);
    const peek = screen.getByTestId("activity-peek");
    expect(peek.textContent).toContain("Read · input");
    expect(peek.textContent).toContain("touched := map[string]bool{}");
    fireEvent.mouseLeave(readRow);
    expect(screen.queryByTestId("activity-peek")).toBeNull();
  });

  it("renders ⊘ interrupted on a historical tool row with no result (T17)", () => {
    render(
      <ActivityGroup
        entries={[{ type: "tool", tool: { id: "t9", name: "Bash", input: { command: "go test ./ui/..." } } }]}
        defaultOpen
      />,
    );
    const row = screen.getByTestId("activity-row");
    expect(row.querySelector('[data-status="interrupted"]')).toBeTruthy();
  });

  it("honors an out-of-band status override (session.tool_status_changed)", () => {
    const statuses = new Map([["t9", "pending" as const]]);
    render(
      <ActivityGroup
        entries={[{ type: "tool", tool: { id: "t9", name: "Bash" } }]}
        statuses={statuses}
        defaultOpen
      />,
    );
    const row = screen.getByTestId("activity-row");
    expect(row.querySelector('[data-status="pending"]')).toBeTruthy();
  });

  it("thinking rows render start-lines and peek the full block", () => {
    render(
      <ActivityGroup
        entries={[{ type: "thinking", text: "the ledger never distinguishes\ncreated vs edited" }]}
        defaultOpen
      />,
    );
    expect(screen.getByText(/Thought · the ledger never distinguishes/)).toBeTruthy();
    fireEvent.mouseEnter(screen.getByTestId("activity-row"));
    expect(screen.getByTestId("activity-peek").textContent).toContain("created vs edited");
  });
});

describe("ActivityGroup — blocked calls and permission cards", () => {
  const denied: ActivityEntry = {
    type: "tool",
    tool: {
      id: "t3",
      name: "Write",
      kind: "edit",
      title: "write /etc/hosts",
      input: { file_path: "/etc/hosts" },
      result: { text: "Permission to use Write has been denied.", is_error: true },
    },
  };
  const blocked = new Map([["t3", "blocked" as const]]);
  const pendingRequest: PermissionRequest = {
    id: "perm-1",
    tool: "Read",
    tool_use_id: "t2",
    status: "pending",
    reason: "Path is outside allowed working directories",
  };

  it("renders a blocked row with its own glyph, not failed's ✕", () => {
    render(<ActivityGroup entries={[denied]} statuses={blocked} defaultOpen />);
    const row = screen.getByTestId("activity-row");
    expect(row.querySelector('[data-status="blocked"]')).toBeTruthy();
    expect(row.querySelector('[data-status="failed"]')).toBeNull();
  });

  it("the header shows blocked when that's the only problem, failed when the group also failed", () => {
    const { unmount } = render(<ActivityGroup entries={[...entries, denied]} statuses={blocked} />);
    const header = screen.getByRole("button", { name: /3 actions/ });
    expect(header.querySelector('[data-status="blocked"]')).toBeTruthy();
    unmount();

    const failing: ActivityEntry = {
      type: "tool",
      tool: { id: "t4", name: "Bash", kind: "execute", title: "make", result: { text: "exit 2", is_error: true } },
    };
    render(<ActivityGroup entries={[denied, failing]} statuses={blocked} />);
    const header2 = screen.getByRole("button", { name: /2 actions/ });
    expect(header2.querySelector('[data-status="failed"]')).toBeTruthy();
    expect(header2.querySelector('[data-status="blocked"]')).toBeNull();
  });

  it("labels a blocked call's result 'blocked' in the peek, not 'error'", () => {
    render(<ActivityGroup entries={[denied]} statuses={blocked} defaultOpen />);
    fireEvent.mouseEnter(screen.getByTestId("activity-row"));
    const labels = [...screen.getByTestId("activity-peek").querySelectorAll("div")].map(
      (d) => d.textContent,
    );
    expect(labels).toContain("blocked");
    expect(labels).not.toContain("error");
  });

  it("renders an anchored permission card right under its call's row", () => {
    const onResolve = vi.fn().mockResolvedValue(undefined);
    render(
      <ActivityGroup
        entries={entries}
        defaultOpen
        permissions={new Map([["t2", [pendingRequest]]])}
        onResolvePermission={onResolve}
      />,
    );
    const [, readRow] = screen.getAllByTestId("activity-row");
    const card = readRow.nextElementSibling!;
    expect(card.textContent).toContain("awaiting your decision");
    expect(card.textContent).toContain("Path is outside allowed working directories");
    fireEvent.click(screen.getByRole("button", { name: "Allow" }));
    expect(onResolve).toHaveBeenCalledWith("perm-1", "allow");
  });

  it("a collapsed group opens itself for a pending request, and flags it if collapsed again", () => {
    const { rerender } = render(<ActivityGroup entries={entries} />);
    const header = screen.getByRole("button", { name: /2 actions/ });
    expect(header.getAttribute("aria-expanded")).toBe("false");

    rerender(<ActivityGroup entries={entries} permissions={new Map([["t2", [pendingRequest]]])} />);
    expect(header.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByRole("button", { name: "Allow" })).toBeTruthy();

    // The user collapses it anyway: the header still says it needs them.
    fireEvent.click(header);
    expect(screen.queryByRole("button", { name: "Allow" })).toBeNull();
    expect(header.textContent).toContain("needs your approval");

    // Decided: no more flag.
    const allowed: PermissionRequest = { ...pendingRequest, status: "allowed" };
    rerender(<ActivityGroup entries={entries} permissions={new Map([["t2", [allowed]]])} />);
    expect(header.textContent).not.toContain("needs your approval");
  });
});
