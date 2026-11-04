import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { ChatRenderer } from "../ChatRenderer/ChatRenderer";
import type { TranscriptMessage } from "../../router/types";
import { FIND_CAP, type FindRequest, type FindStatus } from "./useFind";
import type { KeyedRow } from "../keyed/conversation";

/** Transcript messages as keyed rows: positional keys, and a tool_result
 * folded onto its call's row (`tool.result`), as keyPage does. */
function keyed(ms: TranscriptMessage[]): KeyedRow[] {
  const out: KeyedRow[] = [];
  ms.forEach((m, i) => {
    if (m.role === "tool_result") {
      const call = out.find((r) => r.role === "tool" && r.tool?.id === m.tool?.id);
      if (call?.tool) call.tool = { ...call.tool, result: { text: m.text, is_error: m.is_error, timestamp: m.timestamp } };
      return;
    }
    out.push({ ...m, key: m.key ?? `r${i}` });
  });
  return out;
}

const messages: TranscriptMessage[] = [
  { role: "user", text: "make a worktree for this", timestamp: "2026-10-12T10:00:00Z" },
  {
    role: "tool",
    tool: { id: "tu1", name: "Bash", input: { command: "git worktree add ../x" } },
    timestamp: "2026-10-12T10:00:01Z",
  },
  { role: "tool_result", text: "Preparing worktree (new branch 'x')", tool: { id: "tu1", name: "" } },
  { role: "agent", text: "The worktree is ready.", timestamp: "2026-10-12T10:00:03Z" },
];

function setup(msgs: TranscriptMessage[], find: Partial<FindRequest>) {
  const onStatus = vi.fn<(s: FindStatus) => void>();
  let req: FindRequest = { query: "", matchCase: false, skipTools: false, step: 0, dir: 1, ...find };
  const view = render(<ChatRenderer rows={keyed(msgs)} find={req} onFindStatus={onStatus} />);
  return {
    status: () => onStatus.mock.calls.at(-1)?.[0],
    step(dir: 1 | -1 = 1) {
      req = { ...req, step: req.step + 1, dir };
      view.rerender(<ChatRenderer rows={keyed(msgs)} find={req} onFindStatus={onStatus} />);
    },
    set(next: Partial<FindRequest>) {
      req = { ...req, ...next };
      view.rerender(<ChatRenderer rows={keyed(msgs)} find={req} onFindStatus={onStatus} />);
    },
  };
}

const groupHead = () => screen.getByTestId("activity-group").querySelector("button")!;

describe("find in conversation", () => {
  it("counts matches in prose and in a collapsed group's input and output, in stream order", () => {
    const f = setup(messages, { query: "worktree" });
    expect(f.status()).toMatchObject({ count: 4, current: 0 });
    // Closed, the group says how many it holds.
    expect(groupHead().getAttribute("aria-expanded")).toBe("false");
    expect(screen.getByLabelText("2 matches")).toBeTruthy();
  });

  it("opens the group on its match, shows it in context, and closes it again on moving on", () => {
    const f = setup(messages, { query: "worktree" });
    f.step();
    expect(f.status()).toMatchObject({ current: 1 });
    expect(groupHead().getAttribute("aria-expanded")).toBe("true");
    let snippet = screen.getByTestId("find-snippet");
    expect(snippet.textContent).toBe("input · git worktree add ../x");
    expect(snippet.querySelector("mark")!.textContent).toBe("worktree");

    f.step();
    snippet = screen.getByTestId("find-snippet");
    expect(snippet.textContent).toContain("output · Preparing worktree");

    f.step();
    expect(f.status()).toMatchObject({ current: 3 });
    expect(groupHead().getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByTestId("find-snippet")).toBeNull();

    // Wraps both ways.
    f.step();
    expect(f.status()).toMatchObject({ current: 0 });
    f.step(-1);
    expect(f.status()).toMatchObject({ current: 3 });
  });

  it("leaves tool calls and output out when asked", () => {
    const f = setup(messages, { query: "worktree", skipTools: true });
    expect(f.status()).toMatchObject({ count: 2 });
    expect(screen.queryByLabelText("2 matches")).toBeNull();
  });

  it("matches case only when asked", () => {
    const f = setup(messages, { query: "Worktree" });
    expect(f.status()).toMatchObject({ count: 4 });
    f.set({ matchCase: true });
    expect(f.status()).toMatchObject({ count: 0, current: -1 });
  });

  it("searches a compaction summary behind its disclosure, and opens it on its match", () => {
    const f = setup(
      [
        { role: "compact", text: "We set up a worktree and ran setup.", compact: { trigger: "auto" } },
        { role: "agent", text: "Carrying on with the worktree." },
      ],
      { query: "worktree" },
    );
    expect(f.status()).toMatchObject({ count: 2, current: 0 });
    // Current is in the summary: it shows.
    expect(screen.getByRole("button", { name: "Hide summary" })).toBeTruthy();
    f.step();
    expect(screen.getByRole("button", { name: "Show summary" })).toBeTruthy();
    expect(screen.getByText("1 match")).toBeTruthy();
  });

  it("doesn't count chrome: the Copy button, timestamps, the not-delivered line", () => {
    const f = setup(
      [
        { role: "user", text: "copy this" },
        { role: "agent", text: "API Error: Connection refused", is_error: true },
        { role: "agent", text: "```\ncopy me\n```" },
      ],
      { query: "copy" },
    );
    expect(f.status()).toMatchObject({ count: 2 });
    const f2 = setup([{ role: "user", text: "hi", timestamp: "2026-10-12T10:00:00Z" }], { query: "10" });
    expect(f2.status()).toMatchObject({ count: 0 });
  });

  it("measures a group once however many matches it holds, and ticks it once", () => {
    const output = Array.from({ length: 500 }, (_, i) => `line ${i}: worktree`).join("\n");
    const msgs: TranscriptMessage[] = [
      { role: "tool", tool: { id: "tu1", name: "Bash", input: { command: "ls" } } },
      { role: "tool_result", text: output, tool: { id: "tu1", name: "" } },
    ];
    const rect = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect");
    const f = setup(msgs, { query: "worktree" });
    expect(f.status()).toMatchObject({ count: 500, ticks: [expect.any(Number)] });
    // A few for the scroller and the reveal; never one (or three) per match.
    expect(rect.mock.calls.length).toBeLessThan(50);
    rect.mockRestore();
  });

  describe("past the cap", () => {
    const lines = (n: number) => [
      { role: "tool", tool: { id: "tu1", name: "Bash", input: { command: "ls" } } },
      {
        role: "tool_result",
        text: Array.from({ length: n }, (_, i) => `line ${i}: worktree`).join("\n"),
        tool: { id: "tu1", name: "" },
      },
    ] satisfies TranscriptMessage[];
    // The line the current match is on, from the snippet shown under its row.
    const line = () => {
      const mark = screen.getByTestId("find-snippet").querySelector("mark")!;
      return mark.previousSibling?.textContent?.match(/line (\d+): $/)?.[1];
    };

    it("counts up to the cap exactly", () => {
      const f = setup(lines(FIND_CAP), { query: "worktree" });
      expect(f.status()).toMatchObject({ count: FIND_CAP, capped: false, current: 0 });
    });

    it("says there are more, and steps over every match, wrapping at the ends", () => {
      const f = setup(lines(1500), { query: "worktree" });
      expect(f.status()).toMatchObject({ count: FIND_CAP, capped: true, current: -1 });
      f.step();
      expect(line()).toBe("1");
      f.step(-1);
      f.step(-1);
      expect(line()).toBe("1499");
      f.step();
      expect(line()).toBe("0");
      // Out past the far edge of the first thousand.
      for (let i = 0; i < 1200; i++) f.step();
      expect(line()).toBe("1200");
      expect(f.status()).toMatchObject({ capped: true });
    });
  });

  it("reports nothing, and paints nothing, with no query", () => {
    const f = setup(messages, { query: "worktree" });
    f.set({ query: "" });
    expect(f.status()).toEqual({ count: 0, capped: false, current: -1, ticks: [], currentTick: -1 });
    expect(screen.queryByLabelText("2 matches")).toBeNull();
  });
});
