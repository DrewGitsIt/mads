import { describe, expect, it } from "vitest";
import type { ActivityEntry } from "../activity/conversationItems";
import { findAll, findPattern, searchEntries, snippet, type SearchCache } from "./findText";

const tool = (id: string, input: unknown, output?: string): ActivityEntry => ({
  type: "tool",
  tool: { id, name: "Bash", input, result: output === undefined ? undefined : { text: output } },
});

describe("findPattern / findAll", () => {
  it("is off for an empty query", () => {
    expect(findPattern({ query: "", matchCase: false })).toBeNull();
  });

  it("matches literally — regex characters are text", () => {
    const p = findPattern({ query: "a.b(", matchCase: false })!;
    expect(findAll("axb( a.b( a.b(", p)).toEqual([
      { start: 5, length: 4 },
      { start: 10, length: 4 },
    ]);
  });

  it("ignores case unless asked, with offsets into the original text", () => {
    const text = "Worktree, worktree, WORKTREE";
    expect(findAll(text, findPattern({ query: "worktree", matchCase: false })!)).toHaveLength(3);
    expect(findAll(text, findPattern({ query: "worktree", matchCase: true })!)).toEqual([{ start: 10, length: 8 }]);
  });

  it("can be reused: each search starts from the top", () => {
    const p = findPattern({ query: "x", matchCase: false })!;
    expect(findAll("x x", p)).toHaveLength(2);
    expect(findAll("x x", p)).toHaveLength(2);
  });
});

describe("searchEntries", () => {
  const entries: ActivityEntry[] = [
    tool("t1", { command: "./scripts/setup.sh" }, "error: ENOENT: no such file, open '.env.local'"),
    { type: "thinking", text: "The setup script wants an .env.local" },
    tool("t2", { file_path: "/repo/.env.example" }),
  ];

  it("searches a call's input values and output, and thinking, in row order", () => {
    const found = searchEntries(entries, findPattern({ query: ".env", matchCase: false })!, false);
    expect(found.map((m) => [m.entryKey, m.part])).toEqual([
      ["t1", "output"],
      ["th1", "thinking"],
      ["t2", "input"],
    ]);
  });

  it("leaves the input's keys out — they're the tool's schema, not anything said", () => {
    expect(searchEntries(entries, findPattern({ query: "file_path", matchCase: false })!, false)).toEqual([]);
  });

  it("skips tool calls and output when asked, but keeps thinking", () => {
    const found = searchEntries(entries, findPattern({ query: ".env", matchCase: false })!, true);
    expect(found.map((m) => m.part)).toEqual(["thinking"]);
  });

  it("reuses matches already found for the same text", () => {
    const cache: SearchCache = new Map();
    const p = findPattern({ query: "enoent", matchCase: false })!;
    searchEntries(entries, p, false, cache);
    const output = "error: ENOENT: no such file, open '.env.local'";
    expect(cache.get(output)).toEqual([{ start: 7, length: 6 }]);
    cache.set(output, []);
    expect(searchEntries(entries, p, false, cache)).toEqual([]);
  });
});

describe("snippet", () => {
  it("cuts one line of context around the match, folding whitespace", () => {
    const text = "line one\n\nerror:\n  ENOENT  here and on";
    const start = text.indexOf("ENOENT");
    expect(snippet(text, start, 6, 9)).toEqual({ before: "…error: ", match: "ENOENT", after: " here an…" });
  });

  it("has no ellipsis at the text's own ends", () => {
    expect(snippet("ab ENOENT cd", 3, 6)).toEqual({ before: "ab ", match: "ENOENT", after: " cd" });
  });
});
