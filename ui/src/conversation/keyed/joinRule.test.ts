import { describe, expect, it } from "vitest";
import type { KeyedRow } from "./conversation";
import { joinRows, joinText } from "./joinRule";

const text = (key: string, t: string, extra: Partial<KeyedRow> = {}): KeyedRow => ({ key, role: "agent", text: t, ...extra });

describe("joinText (the repro cases R4A–E)", () => {
  it.each([
    ["R4A sentence then heading", "I'll check the config files first.", "## Config\n\nAll good.", "I'll check the config files first.\n\n## Config\n\nAll good."],
    ["R4B a list split across items", "Steps:\n\n- first\n- second\n", "- third\n- fourth\n\nDone.", "Steps:\n\n- first\n- second\n- third\n- fourth\n\nDone."],
    ["R4C two sentences", "One.", "Two.", "One.\n\nTwo."],
    ["R4D sentence then list", "Here:", "- a\n- b", "Here:\n\n- a\n- b"],
    ["R4E item 2 starts with a blank line", "Done.\n", "\nNext.", "Done.\n\nNext."],
  ])("%s", (_name, a, b, want) => {
    expect(joinText(a, b)).toBe(want);
  });
});

describe("joinRows", () => {
  it("joins adjacent text rows regardless of message, keyed by the first", () => {
    const out = joinRows([text("m1:0", "a"), text("m2:0", "b")]);
    expect(out).toHaveLength(1);
    expect(out[0]).toMatchObject({ key: "m1:0", text: "a\n\nb", joined: ["m1:0", "m2:0"] });
  });

  it("hidden thinking between texts doesn't break the bubble, and isn't rendered", () => {
    const out = joinRows([text("a", "x"), { key: "t", role: "thinking", text: "  " }, text("b", "y")]);
    expect(out.map((r) => r.key)).toEqual(["a"]);
    expect(out[0].text).toBe("x\n\ny");
  });

  it.each<[string, KeyedRow]>([
    ["a tool call", { key: "t", role: "tool", tool: { name: "ls" } }],
    ["thinking with text", { key: "t", role: "thinking", text: "hmm" }],
    ["a notice", text("n", "interrupted", { notice: true })],
    ["a user message", { key: "u", role: "user", text: "hi" }],
  ])("%s ends the bubble", (_name, between) => {
    const out = joinRows([text("a", "x"), between, text("b", "y")]);
    expect(out.map((r) => r.key)).toEqual(["a", between.key, "b"]);
  });

  it("an empty text row joins without a separator", () => {
    expect(joinRows([text("a", "x"), text("b", "")])[0].text).toBe("x");
  });

  it("doesn't mutate its input", () => {
    const rows = [text("a", "x"), text("b", "y")];
    joinRows(rows);
    expect(rows[0].text).toBe("x");
  });

  it("live and stored rows of the same items join to the same text", () => {
    const live = joinRows([text("a", "Steps:\n\n- one\n", { live: true }), text("b", "- two", { live: true })]);
    const stored = joinRows([text("a", "Steps:\n\n- one\n"), text("b", "- two")]);
    expect(live[0].text).toBe(stored[0].text);
  });
});
