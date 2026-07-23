import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join, dirname } from "node:path";

const css = readFileSync(join(dirname(__filename), "ProposalCard.module.css"), "utf8");

describe("ProposalCard border pulse — reduced motion", () => {
  it("pulses new cards via a keyframe animation", () => {
    expect(css).toMatch(/\.card\s*{[^}]*animation:\s*proposal-pulse/);
    expect(css).toMatch(/@keyframes proposal-pulse/);
  });

  it("disables the pulse animation under prefers-reduced-motion: reduce", () => {
    const reducedMotionBlock = css.match(
      /@media \(prefers-reduced-motion: reduce\)\s*{([^}]*{[^}]*})*[^}]*}/,
    )?.[0];
    expect(reducedMotionBlock).toBeTruthy();
    expect(reducedMotionBlock).toMatch(/\.card\s*{\s*animation:\s*none;\s*}/);
  });
});
