import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { ChatRenderer } from "../ChatRenderer/ChatRenderer";
import type { TranscriptMessage } from "../../router/types";
import type { FindRequest, FindStatus } from "./useFind";

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
  const view = render(<ChatRenderer messages={msgs} find={req} onFindStatus={onStatus} />);
  return {
    status: () => onStatus.mock.calls.at(-1)?.[0],
    step(dir: 1 | -1 = 1) {
      req = { ...req, step: req.step + 1, dir };
      view.rerender(<ChatRenderer messages={msgs} find={req} onFindStatus={onStatus} />);
    },
    set(next: Partial<FindRequest>) {
      req = { ...req, ...next };
      view.rerender(<ChatRenderer messages={msgs} find={req} onFindStatus={onStatus} />);
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

  it("reports nothing, and paints nothing, with no query", () => {
    const f = setup(messages, { query: "worktree" });
    f.set({ query: "" });
    expect(f.status()).toEqual({ count: 0, current: -1, ticks: [], currentTick: -1 });
    expect(screen.queryByLabelText("2 matches")).toBeNull();
  });
});
