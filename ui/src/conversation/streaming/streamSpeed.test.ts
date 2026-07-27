import { describe, expect, it } from "vitest";
import { normalizeStreamSpeed, streamSpeedToCharsPerSec } from "./streamSpeed";

// Regression for the 2026-07-27 renderer freeze: a config row that predates
// stream_speed (or any unrecognized value) must resolve to passthrough/"off"
// rather than an out-of-domain string that later produces NaN in the pacer.
describe("normalizeStreamSpeed", () => {
  it.each([
    ["", "off"],
    ["bogus", "off"],
    [null, "off"],
    [undefined, "off"],
    ["off", "off"],
    ["slow", "slow"],
    ["medium", "medium"],
    ["fast", "fast"],
  ] as const)("normalizes %p to %p", (input, expected) => {
    expect(normalizeStreamSpeed(input)).toBe(expected);
  });
});

describe("streamSpeedToCharsPerSec", () => {
  it("is total: an out-of-domain key returns null (passthrough) instead of undefined", () => {
    expect(streamSpeedToCharsPerSec("" as never)).toBeNull();
    expect(streamSpeedToCharsPerSec("bogus" as never)).toBeNull();
  });

  it("returns null for off and a positive rate for the rest", () => {
    expect(streamSpeedToCharsPerSec("off")).toBeNull();
    expect(streamSpeedToCharsPerSec("slow")).toBeGreaterThan(0);
    expect(streamSpeedToCharsPerSec("medium")).toBeGreaterThan(0);
    expect(streamSpeedToCharsPerSec("fast")).toBeGreaterThan(0);
  });
});
