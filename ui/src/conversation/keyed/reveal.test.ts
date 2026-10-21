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
});
