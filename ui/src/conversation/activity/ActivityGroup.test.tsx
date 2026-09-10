import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { ActivityGroup } from "./ActivityGroup";
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

  it("flips the peek above the row when the downward card would overflow the bottom", () => {
    // jsdom has no layout: every rect is 0, so the default peek never
    // crosses the boundary. Force the overflow case, then the normal case.
    const spy = vi
      .spyOn(HTMLElement.prototype, "getBoundingClientRect")
      .mockReturnValue({ bottom: window.innerHeight + 200 } as DOMRect);
    const { unmount } = render(<ActivityGroup entries={entries} defaultOpen />);
    fireEvent.mouseEnter(screen.getAllByTestId("activity-row")[0]);
    expect(screen.getByTestId("activity-peek").getAttribute("data-up")).toBe("true");
    unmount();
    spy.mockReturnValue({ bottom: 0 } as DOMRect);
    render(<ActivityGroup entries={entries} defaultOpen />);
    fireEvent.mouseEnter(screen.getAllByTestId("activity-row")[0]);
    expect(screen.getByTestId("activity-peek").getAttribute("data-up")).toBeNull();
    spy.mockRestore();
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
