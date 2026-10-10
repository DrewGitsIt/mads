import { describe, expect, it } from "vitest";
import {
  applyToolResult,
  drainAll,
  emptyPacer,
  enqueueText,
  enqueueTool,
  hasPending,
  pendingWordsAtLeast,
  revealedToMessages,
  step,
} from "./streamPacer";
import type { StreamItem } from "../ChatRenderer/ChatRenderer";

function text(items: StreamItem[]): string {
  return items
    .filter((i): i is { kind: "text"; text: string } => i.kind === "text")
    .map((i) => i.text)
    .join("");
}

describe("streamPacer", () => {
  it("reveals a bounded number of words per step, punctuation glued to its word", () => {
    let s = enqueueText(emptyPacer(), "Hello, big wide world. ");
    s = step(s, 1);
    expect(text(s.items)).toBe("Hello,");
    s = step(s, 2);
    expect(text(s.items)).toBe("Hello, big wide");
    s = step(s, 5);
    expect(text(s.items)).toBe("Hello, big wide world.");
  });

  it("accumulates fractional budget across ticks for sub-1-word rates", () => {
    let s = enqueueText(emptyPacer(), "one two three ");
    s = step(s, 0.5); // 0.5 → floor 0, carry 0.5
    expect(text(s.items)).toBe("");
    s = step(s, 0.5); // 0.5 + 0.5 = 1.0 → reveal 1
    expect(text(s.items)).toBe("one");
  });

  it("holds a word touching the end of the buffer until more text or the turn's end", () => {
    let s = enqueueText(emptyPacer(), "streaming tok");
    s = step(s, 10);
    // "tok" may be the front half of "tokens" — never flash a fragment.
    expect(text(s.items)).toBe("streaming");
    s = enqueueText(s, "ens arrive");
    s = step(s, 10);
    expect(text(s.items)).toBe("streaming tokens");
    s = step(s, 10, true); // turn finished: the last word is whole
    expect(text(s.items)).toBe("streaming tokens arrive");
    expect(hasPending(s)).toBe(false);
  });

  it("keeps tokens in one growing run and never resets it", () => {
    let s = emptyPacer();
    s = enqueueText(s, "Hel");
    s = enqueueText(s, "lo");
    s = step(s, 100, true);
    expect(s.items).toHaveLength(1);
    expect(text(s.items)).toBe("Hello");
  });

  it("reveals a fenced code block whole, as one word, once its closing fence arrives", () => {
    let s = enqueueText(emptyPacer(), "Run this:\n```sh\nmake check\ngo test ./...\n");
    s = step(s, 10);
    expect(text(s.items)).toBe("Run this:\n"); // the block waits for its fence
    s = enqueueText(s, "```\nThen ship it.");
    s = step(s, 1);
    expect(text(s.items)).toBe("Run this:\n```sh\nmake check\ngo test ./...\n```\n");
    s = step(s, 10, true);
    expect(text(s.items)).toContain("Then ship it.");
  });

  it("reveals an unclosed code block whole at the turn's end", () => {
    let s = enqueueText(emptyPacer(), "```\nhalf a block");
    s = step(s, 1, true);
    expect(text(s.items)).toBe("```\nhalf a block");
  });

  it("counts words in scripts without spaces (Japanese)", () => {
    let s = enqueueText(emptyPacer(), "東京は日本の首都です。");
    s = step(s, 1);
    const first = text(s.items);
    expect(first.length).toBeGreaterThan(0);
    expect(first.length).toBeLessThan("東京は日本の首都です。".length);
  });

  it("paces a whole-answer burst over many steps instead of at once", () => {
    // The Opus 5.5 shape: ~2,200 characters landing within 0.7s.
    const burst = Array.from({ length: 25 }, (_, i) => `Line ${i} explains one more part of the idea here.`).join("\n");
    let s = enqueueText(emptyPacer(), burst);
    const wordsPerFrame = 500 / 60 / 60; // 500 wpm at 60fps
    let frames = 0;
    while (hasPending(s) && frames < 1_000_000) {
      s = step(s, wordsPerFrame, true);
      frames++;
    }
    expect(text(s.items)).toBe(burst);
    // ~225 words at 500 wpm ≈ 27s — nowhere near the burst's 0.7s.
    expect(frames / 60).toBeGreaterThan(20);
  });

  it("pops a tool card in order, splitting the text around it", () => {
    let s = emptyPacer();
    s = enqueueText(s, "before");
    s = enqueueTool(s, { id: "t1", name: "Read" });
    s = enqueueText(s, "after");
    s = step(s, 1000, true);
    expect(s.items.map((i) => i.kind)).toEqual(["text", "tool", "text"]);
    expect(text(s.items)).toBe("beforeafter");
  });

  it("does not reveal a tool before the text ahead of it has drained", () => {
    let s = emptyPacer();
    s = enqueueText(s, "abc def");
    s = enqueueTool(s, { id: "t1", name: "Read" });
    s = step(s, 1); // only "abc" fits — tool must wait
    expect(s.items.map((i) => i.kind)).toEqual(["text"]);
    s = step(s, 1); // "def" drains, then the tool pops for free
    expect(s.items.map((i) => i.kind)).toEqual(["text", "tool"]);
  });

  it("attaches a tool result to its card whether revealed or still queued", () => {
    let s = emptyPacer();
    s = enqueueTool(s, { id: "t1", name: "Bash" });
    // still queued
    s = applyToolResult(s, "t1", { text: "ok", is_error: false });
    s = step(s, 1000);
    const tool = s.items.find((i) => i.kind === "tool");
    expect(tool?.kind === "tool" && tool.tool.result?.text).toBe("ok");

    // already revealed
    let s2 = step(enqueueTool(emptyPacer(), { id: "t2", name: "Bash" }), 1000);
    s2 = applyToolResult(s2, "t2", { text: "done" });
    const tool2 = s2.items.find((i) => i.kind === "tool");
    expect(tool2?.kind === "tool" && tool2.tool.result?.text).toBe("done");
  });

  it("carries arrival timestamps through reveal and result attachment (duration fuel)", () => {
    let s = enqueueTool(emptyPacer(), { id: "t1", name: "Bash" }, "2026-09-09T10:00:00Z");
    s = step(s, 1000);
    s = applyToolResult(s, "t1", { text: "ok", timestamp: "2026-09-09T10:00:02Z" });
    const tool = s.items.find((i) => i.kind === "tool");
    // the entry keeps its start stamp after the result patch, and the
    // result carries its end stamp — losing either kills row durations
    expect(tool?.kind === "tool" && tool.timestamp).toBe("2026-09-09T10:00:00Z");
    expect(tool?.kind === "tool" && tool.tool.result?.timestamp).toBe("2026-09-09T10:00:02Z");
  });

  it("drainAll reveals the whole queue at once (off / fast-finish)", () => {
    let s = emptyPacer();
    s = enqueueText(s, "one");
    s = enqueueTool(s, { id: "t1", name: "Read" });
    s = enqueueText(s, "two");
    s = drainAll(s);
    expect(text(s.items)).toBe("onetwo");
    expect(s.items.map((i) => i.kind)).toEqual(["text", "tool", "text"]);
    expect(hasPending(s)).toBe(false);
  });

  it("reports whether at least n words wait, across text runs", () => {
    let s = enqueueText(emptyPacer(), "one two");
    s = enqueueTool(s, { id: "t1", name: "Read" });
    s = enqueueText(s, "three");
    expect(pendingWordsAtLeast(s, 3)).toBe(true); // tools don't count
    expect(pendingWordsAtLeast(s, 4)).toBe(false);
  });

  // Regression for the 2026-07-27 renderer freeze: an invalid stream_speed
  // upstream produced a NaN budget, and `take <= 0` is false for NaN, so the
  // old loop guard never broke — an unbounded synchronous while loop.
  describe("non-finite budgets never hang (2026-07-27 freeze regression)", () => {
    it("terminates without draining on a NaN budget", () => {
      let s = enqueueText(emptyPacer(), "hello");
      s = step(s, NaN);
      expect(text(s.items)).toBe("");
      expect(hasPending(s)).toBe(true);
      expect(Number.isFinite(s.carry)).toBe(true);
    });

    it("terminates without draining on an undefined-coerced budget", () => {
      let s = enqueueText(emptyPacer(), "hello");
      s = step(s, undefined as unknown as number);
      expect(text(s.items)).toBe("");
      expect(hasPending(s)).toBe(true);
    });

    it("drains fully on an Infinity budget", () => {
      let s = enqueueText(emptyPacer(), "hello");
      s = enqueueTool(s, { id: "t1", name: "Read" });
      s = enqueueText(s, "world");
      s = step(s, Infinity, true);
      expect(text(s.items)).toBe("helloworld");
      expect(hasPending(s)).toBe(false);
    });

    it("a NaN step does not poison carry for a later, legitimate step", () => {
      let s = enqueueText(emptyPacer(), "hello");
      s = step(s, NaN);
      s = step(s, 5, true);
      expect(text(s.items)).toBe("hello");
      expect(hasPending(s)).toBe(false);
    });
  });

  it("revealedToMessages hands revealed text and tools to the history, in order", () => {
    expect(
      revealedToMessages([
        { kind: "text", text: "Looking." },
        { kind: "tool", tool: { id: "t1", name: "Read", result: { text: "ok" } }, timestamp: "2026-10-09T10:00:00Z" },
        { kind: "thinking", text: "hmm" },
        { kind: "text", text: "Done." },
      ]),
    ).toEqual([
      { role: "agent", text: "Looking." },
      {
        role: "tool",
        tool: { id: "t1", name: "Read", result: { text: "ok" } },
        timestamp: "2026-10-09T10:00:00Z",
      },
      { role: "agent", text: "Done." },
    ]);
  });
});
