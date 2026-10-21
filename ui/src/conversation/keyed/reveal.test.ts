import { describe, expect, it } from "vitest";
import type { KeyedRow } from "./conversation";
import { emptyReveal, pendingWordsAtLeast, revealAll, revealedRows, revealPending, revealStep } from "./reveal";

const paced = (key: string, text: string, open = false): KeyedRow => ({ key, role: "agent", text, paced: true, live: true, open });

describe("reveal", () => {
  it("shows replayed and stored rows whole", () => {
    const rows: KeyedRow[] = [{ key: "a", role: "agent", text: "all at once" }];
    expect(revealedRows(rows, emptyReveal())).toEqual(rows);
    expect(revealPending(rows, emptyReveal())).toBe(false);
  });

  it("reveals paced text word by word and holds rows after the gate", () => {
    const rows: KeyedRow[] = [paced("a", "one two three"), { key: "t", role: "tool", tool: { name: "ls" }, live: true }];
    let p = revealStep(rows, emptyReveal(), 1);
    expect(revealedRows(rows, p).map((r) => r.text ?? r.role)).toEqual(["one"]);
    p = revealStep(rows, p, 5);
    expect(revealedRows(rows, p).map((r) => r.text ?? r.role)).toEqual(["one two three", "tool"]);
  });

  it("never holds back the user's own message", () => {
    const rows: KeyedRow[] = [paced("a", "one two"), { key: "u", role: "user", text: "mid-turn", live: true }];
    expect(revealedRows(rows, emptyReveal()).map((r) => r.key)).toEqual(["a", "u"]);
  });

  it("holds a fragment of an open item until it stops", () => {
    const rows = [paced("a", "one tw", true)];
    const p = revealStep(rows, emptyReveal(), 10);
    expect(revealedRows(rows, p)[0].text).toBe("one");
    const closed = [paced("a", "one tw", false)];
    expect(revealedRows(closed, revealStep(closed, p, 10))[0].text).toBe("one tw");
  });

  it("keeps the reveal when the stored row replaces the live one under the same key", () => {
    const live = [paced("a", "one two three")];
    const p = revealStep(live, emptyReveal(), 1);
    const stored: KeyedRow[] = [{ key: "a", role: "agent", text: "one two three", paced: true }];
    expect(revealedRows(stored, p)[0].text).toBe("one");
  });

  it("revealAll and the behind check", () => {
    const rows = [paced("a", "one two three four")];
    expect(pendingWordsAtLeast(rows, emptyReveal(), 3)).toBe(true);
    const p = revealAll(rows, emptyReveal());
    expect(revealPending(rows, p)).toBe(false);
    expect(pendingWordsAtLeast(rows, p, 1)).toBe(false);
  });

  // Migrated from streaming/streamPacer.test.ts: the word, fence and budget
  // rules the reveal kept.
  const shownText = (rows: KeyedRow[], p: ReturnType<typeof emptyReveal>) =>
    revealedRows(rows, p)
      .filter((r) => r.role === "agent")
      .map((r) => r.text)
      .join("|");

  it("reveals a bounded number of words per step, punctuation glued to its word", () => {
    const rows = [paced("a", "Hello, big wide world. ", true)];
    let p = revealStep(rows, emptyReveal(), 1);
    expect(shownText(rows, p)).toBe("Hello,");
    p = revealStep(rows, p, 2);
    expect(shownText(rows, p)).toBe("Hello, big wide");
    p = revealStep(rows, p, 5);
    expect(shownText(rows, p)).toBe("Hello, big wide world.");
  });

  it("accumulates fractional budget across ticks for sub-1-word rates", () => {
    const rows = [paced("a", "one two three ", true)];
    let p = revealStep(rows, emptyReveal(), 0.5); // floor 0, carry 0.5
    expect(shownText(rows, p)).toBe("");
    p = revealStep(rows, p, 0.5); // 0.5 + 0.5 = 1 word
    expect(shownText(rows, p)).toBe("one");
  });

  it("holds a word touching the end of a streaming row until more text or the turn's end", () => {
    let rows = [paced("a", "streaming tok", true)];
    let p = revealStep(rows, emptyReveal(), 10);
    // "tok" may be the front half of "tokens" — never flash a fragment.
    expect(shownText(rows, p)).toBe("streaming");
    rows = [paced("a", "streaming tokens arrive", true)];
    p = revealStep(rows, p, 10);
    expect(shownText(rows, p)).toBe("streaming tokens");
    p = revealStep(rows, p, 10, true); // the turn finished: the last word is whole
    expect(shownText(rows, p)).toBe("streaming tokens arrive");
    expect(revealPending(rows, p)).toBe(false);
  });

  it("reveals a fenced code block whole, as one word, once its closing fence arrives", () => {
    let rows = [paced("a", "Run this:\n```sh\nmake check\ngo test ./...\n", true)];
    let p = revealStep(rows, emptyReveal(), 10);
    expect(shownText(rows, p)).toBe("Run this:\n"); // the block waits for its fence
    rows = [paced("a", "Run this:\n```sh\nmake check\ngo test ./...\n```\nThen ship it.", true)];
    p = revealStep(rows, p, 1);
    expect(shownText(rows, p)).toBe("Run this:\n```sh\nmake check\ngo test ./...\n```\n");
    p = revealStep(rows, p, 10, true);
    expect(shownText(rows, p)).toContain("Then ship it.");
  });

  it("reveals an unclosed code block whole at the turn's end", () => {
    const rows = [paced("a", "```\nhalf a block", true)];
    expect(shownText(rows, revealStep(rows, emptyReveal(), 1, true))).toBe("```\nhalf a block");
  });

  it("counts words in scripts without spaces (Japanese)", () => {
    const text = "東京は日本の首都です。";
    const rows = [paced("a", text, true)];
    const first = shownText(rows, revealStep(rows, emptyReveal(), 1));
    expect(first.length).toBeGreaterThan(0);
    expect(first.length).toBeLessThan(text.length);
  });

  it("paces a whole-answer burst over many steps instead of at once", () => {
    // The Opus 5.5 shape: ~2,200 characters landing within 0.7s.
    const burst = Array.from({ length: 25 }, (_, i) => `Line ${i} explains one more part of the idea here.`).join("\n");
    const rows = [paced("a", burst)];
    const wordsPerFrame = 500 / 60 / 60; // 500 wpm at 60fps
    let p = emptyReveal();
    let frames = 0;
    while (revealPending(rows, p) && frames < 1_000_000) {
      p = revealStep(rows, p, wordsPerFrame, true);
      frames++;
    }
    expect(shownText(rows, p)).toBe(burst);
    // ~225 words at 500 wpm ≈ 27s — nowhere near the burst's 0.7s.
    expect(frames / 60).toBeGreaterThan(20);
  });

  it("pops a tool between two text rows in order, once the text before it drained", () => {
    const rows: KeyedRow[] = [
      paced("a", "abc def"),
      { key: "t", role: "tool", tool: { id: "t1", name: "Read" }, live: true },
      paced("b", "after"),
    ];
    let p = revealStep(rows, emptyReveal(), 1); // only "abc" fits — the tool waits
    expect(revealedRows(rows, p).map((r) => r.key)).toEqual(["a"]);
    p = revealStep(rows, p, 1); // "def" drains, the tool pops for free, "after" starts
    expect(revealedRows(rows, p).map((r) => r.key)).toEqual(["a", "t", "b"]);
    p = revealStep(rows, p, 10, true);
    expect(shownText(rows, p)).toBe("abc def|after");
  });

  it("counts waiting words across text rows; tools don't count", () => {
    const rows: KeyedRow[] = [
      paced("a", "one two"),
      { key: "t", role: "tool", tool: { id: "t1", name: "Read" }, live: true },
      paced("b", "three"),
    ];
    expect(pendingWordsAtLeast(rows, emptyReveal(), 3)).toBe(true);
    expect(pendingWordsAtLeast(rows, emptyReveal(), 4)).toBe(false);
  });

  // Regression for the 2026-07-27 renderer freeze: an invalid stream_speed
  // upstream produced a NaN budget, and `take <= 0` is false for NaN, so the
  // old loop guard never broke — an unbounded synchronous while loop.
  describe("non-finite budgets never hang (2026-07-27 freeze regression)", () => {
    const rows = [paced("a", "hello")];

    it("terminates without revealing on a NaN budget", () => {
      const p = revealStep(rows, emptyReveal(), NaN);
      expect(shownText(rows, p)).toBe("");
      expect(revealPending(rows, p)).toBe(true);
      expect(Number.isFinite(p.carry)).toBe(true);
    });

    it("terminates without revealing on an undefined-coerced budget", () => {
      const p = revealStep(rows, emptyReveal(), undefined as unknown as number);
      expect(shownText(rows, p)).toBe("");
      expect(revealPending(rows, p)).toBe(true);
    });

    it("reveals everything on an Infinity budget", () => {
      const two: KeyedRow[] = [paced("a", "hello"), { key: "t", role: "tool", tool: { name: "Read" }, live: true }, paced("b", "world")];
      const p = revealStep(two, emptyReveal(), Infinity, true);
      expect(shownText(two, p)).toBe("hello|world");
      expect(revealPending(two, p)).toBe(false);
    });

    it("a NaN step does not poison carry for a later, legitimate step", () => {
      let p = revealStep(rows, emptyReveal(), NaN);
      p = revealStep(rows, p, 5, true);
      expect(shownText(rows, p)).toBe("hello");
      expect(revealPending(rows, p)).toBe(false);
    });
  });
});
