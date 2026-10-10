import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { applyMotion } from "./applyMotion";

describe("applyMotion", () => {
  it("marks the root when on and clears it otherwise", () => {
    applyMotion(true);
    expect(document.documentElement.dataset.motion).toBe("reduce");
    applyMotion(false);
    expect(document.documentElement.dataset.motion).toBeUndefined();
    applyMotion(undefined);
    expect(document.documentElement.dataset.motion).toBeUndefined();
  });

  // The attribute only works if the global kill-switch matches it as well as
  // the OS media query.
  it("is matched by the global reduced-motion rule", () => {
    const css = readFileSync("src/styles/global.css", "utf8");
    expect(css).toMatch(
      /:root\[data-motion="reduce"\] \*,[\s\S]*?\{\s*transition: none !important;\s*animation: none !important;/,
    );
  });
});
