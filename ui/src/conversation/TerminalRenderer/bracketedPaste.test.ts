import { describe, expect, it } from "vitest";
import { formatBracketedPaste } from "./bracketedPaste";

describe("formatBracketedPaste", () => {
  it("wraps text in the standard xterm bracketed-paste escape sequence", () => {
    expect(formatBracketedPaste("hello")).toBe("\x1b[200~hello\x1b[201~");
  });

  it("preserves multi-line and special-character content verbatim", () => {
    const text = "line one\nline two\t🎉";
    expect(formatBracketedPaste(text)).toBe(`\x1b[200~${text}\x1b[201~`);
  });
});
