import { describe, expect, it } from "vitest";
import {
  DEFAULT_CUSTOM_WPM,
  MAX_STREAM_WPM,
  MIN_STREAM_WPM,
  STREAM_WPM_PRESETS,
  normalizeStreamSpeed,
  streamPaceToWpm,
} from "./streamSpeed";

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
    ["medium", "fast"], // retired preset
    ["fast", "fast"],
    ["custom", "custom"],
  ] as const)("normalizes %p to %p", (input, expected) => {
    expect(normalizeStreamSpeed(input)).toBe(expected);
  });
});

describe("streamPaceToWpm", () => {
  it("is total: an out-of-domain speed returns null (passthrough) instead of undefined", () => {
    expect(streamPaceToWpm({ speed: "" as never })).toBeNull();
    expect(streamPaceToWpm({ speed: "bogus" as never })).toBeNull();
  });

  it("returns null for off and the presets' pace for slow and fast", () => {
    expect(streamPaceToWpm({ speed: "off" })).toBeNull();
    expect(streamPaceToWpm({ speed: "slow" })).toBe(STREAM_WPM_PRESETS.slow);
    expect(streamPaceToWpm({ speed: "fast" })).toBe(STREAM_WPM_PRESETS.fast);
  });

  it("reads custom's wpm, defaulting when unset and clamping out-of-range values", () => {
    expect(streamPaceToWpm({ speed: "custom", wpm: 420 })).toBe(420);
    expect(streamPaceToWpm({ speed: "custom" })).toBe(DEFAULT_CUSTOM_WPM);
    expect(streamPaceToWpm({ speed: "custom", wpm: Number.NaN })).toBe(DEFAULT_CUSTOM_WPM);
    expect(streamPaceToWpm({ speed: "custom", wpm: 5 })).toBe(MIN_STREAM_WPM);
    expect(streamPaceToWpm({ speed: "custom", wpm: 99999 })).toBe(MAX_STREAM_WPM);
  });
});
