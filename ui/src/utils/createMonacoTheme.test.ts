import { describe, expect, it } from "vitest";
import { tokenHex } from "./createMonacoTheme";

// Monaco's theme colors parse hex only and turn anything else into pure red
// (Color.fromHex), so every token must arrive as #rrggbbaa.
describe("tokenHex", () => {
  it("passes hex through as #rrggbbaa", () => {
    expect(tokenHex("#1c160c")).toBe("#1c160cff");
    expect(tokenHex("#abc")).toBe("#aabbccff");
    expect(tokenHex("#11223380")).toBe("#11223380");
  });

  it("converts rgb() / rgba(), the form --accent-soft uses", () => {
    expect(tokenHex("rgba(193,139,96,0.16)")).toBe("#c18b6029");
    expect(tokenHex("rgb(10, 20, 30)")).toBe("#0a141eff");
    expect(tokenHex("rgb(10 20 30 / 50%)")).toBe("#0a141e80");
  });

  it("scales alpha for tints of a solid token", () => {
    expect(tokenHex("#76a35c", 0.14)).toBe("#76a35c24");
  });

  it("returns undefined for what it can't parse, so the base theme's color stays", () => {
    expect(tokenHex("")).toBeUndefined();
    expect(tokenHex("oklch(0.7 0.1 140)")).toBeUndefined();
    expect(tokenHex("#12345")).toBeUndefined();
  });
});
