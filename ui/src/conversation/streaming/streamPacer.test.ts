import { describe, expect, it } from "vitest";
import {
  applyToolResult,
  drainAll,
  emptyPacer,
  enqueueText,
  enqueueTool,
  hasPending,
  pendingChars,
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
  it("reveals text a bounded number of characters per step", () => {
    let s = enqueueText(emptyPacer(), "Hello, world");
    s = step(s, 5);
    expect(text(s.items)).toBe("Hello");
    s = step(s, 5);
    expect(text(s.items)).toBe("Hello, wor");
    s = step(s, 5);
    expect(text(s.items)).toBe("Hello, world");
    expect(hasPending(s)).toBe(false);
  });

  it("accumulates fractional budget across ticks for sub-1-char rates", () => {
    let s = enqueueText(emptyPacer(), "abcd");
    s = step(s, 0.5); // 0.5 → floor 0, carry 0.5
    expect(text(s.items)).toBe("");
    s = step(s, 0.5); // 0.5 + 0.5 = 1.0 → reveal 1
    expect(text(s.items)).toBe("a");
  });

  it("keeps tokens in one growing run and never resets it", () => {
    let s = emptyPacer();
    s = enqueueText(s, "Hel");
    s = enqueueText(s, "lo");
    s = step(s, 100);
    expect(s.items).toHaveLength(1);
    expect(text(s.items)).toBe("Hello");
  });

  it("pops a tool card in order, splitting the text around it", () => {
    let s = emptyPacer();
    s = enqueueText(s, "before");
    s = enqueueTool(s, { id: "t1", name: "Read" });
    s = enqueueText(s, "after");
    s = step(s, 1000);
    expect(s.items.map((i) => i.kind)).toEqual(["text", "tool", "text"]);
    expect(text(s.items)).toBe("beforeafter");
  });

  it("does not reveal a tool before the text ahead of it has drained", () => {
    let s = emptyPacer();
    s = enqueueText(s, "abcdef");
    s = enqueueTool(s, { id: "t1", name: "Read" });
    s = step(s, 3); // only "abc" fits — tool must wait
    expect(s.items.map((i) => i.kind)).toEqual(["text"]);
    s = step(s, 3); // "def" drains, then the tool pops for free
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

  it("reports pending characters for backlog-bounded pacing", () => {
    let s = enqueueText(emptyPacer(), "abcde");
    s = enqueueTool(s, { id: "t1", name: "Read" });
    s = enqueueText(s, "fg");
    expect(pendingChars(s)).toBe(7); // tools don't count
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
      s = step(s, Infinity);
      expect(text(s.items)).toBe("helloworld");
      expect(hasPending(s)).toBe(false);
    });

    it("a NaN step does not poison carry for a later, legitimate step", () => {
      let s = enqueueText(emptyPacer(), "hello");
      s = step(s, NaN);
      s = step(s, 5);
      expect(text(s.items)).toBe("hello");
      expect(hasPending(s)).toBe(false);
    });
  });
});
