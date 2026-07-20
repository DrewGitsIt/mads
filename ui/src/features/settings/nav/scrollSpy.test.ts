import { describe, expect, it } from "vitest";
import { computeActiveSection, isAtBottom } from "./scrollSpy";
import type { SectionTop } from "./scrollSpy";

const sections: SectionTop[] = [
  { id: "profiles", top: 0 },
  { id: "general", top: 400 },
  { id: "pipeline", top: 900 },
  { id: "appearance", top: 1500 },
  { id: "credentials", top: 2100 },
  { id: "advanced", top: 2700 },
  { id: "providers", top: 3300 },
  { id: "updates", top: 3400 }, // short trailing section, top near the end
];

describe("isAtBottom", () => {
  it("is false with room left to scroll", () => {
    expect(isAtBottom({ scrollTop: 0, clientHeight: 800, scrollHeight: 3500 })).toBe(false);
  });

  it("is true once the viewport reaches the scrollable bottom", () => {
    expect(isAtBottom({ scrollTop: 2700, clientHeight: 800, scrollHeight: 3500 })).toBe(true);
  });
});

describe("computeActiveSection", () => {
  it("returns null with no sections", () => {
    expect(computeActiveSection({ scrollTop: 0, clientHeight: 800, scrollHeight: 0 }, [])).toBeNull();
  });

  it("defaults to the first section at the top of the page", () => {
    const active = computeActiveSection({ scrollTop: 0, clientHeight: 800, scrollHeight: 3500 }, sections);
    expect(active).toBe("profiles");
  });

  it("picks the last section whose top has passed the 30%-viewport reading line", () => {
    // reading line = 900 + 800*0.3 = 1140 -> pipeline (900) qualifies, appearance (1500) doesn't
    const active = computeActiveSection({ scrollTop: 900, clientHeight: 800, scrollHeight: 3500 }, sections);
    expect(active).toBe("pipeline");
  });

  it("advances as the reading line passes each section top", () => {
    // reading line = 1300 + 240 = 1540 -> appearance (1500) qualifies
    const active = computeActiveSection({ scrollTop: 1300, clientHeight: 800, scrollHeight: 3500 }, sections);
    expect(active).toBe("appearance");
  });

  it("regression: marks the last section active at page bottom, even though its top never reaches the reading line", () => {
    // scrolled to the very bottom; reading line = 2700 + 800*0.3 = 2940,
    // which is past "advanced" (2700) but short of "updates" (3400) — the
    // reading-line rule alone would pick "advanced", never reaching "updates".
    const metrics = { scrollTop: 2700, clientHeight: 800, scrollHeight: 3500 };
    expect(computeActiveSection(metrics, sections)).toBe("updates");
  });
});
