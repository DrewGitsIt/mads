import { describe, expect, it } from "vitest";
import { modelDisplayName } from "./modelDisplay";
import type { Model } from "./types";

const models: Model[] = [
  { id: "claude-sonnet-5", name: "Claude Sonnet 5", default: true },
  { id: "claude-opus-4-8", name: "Claude Opus 4.8" },
];

describe("modelDisplayName", () => {
  it("resolves a known model ID to its display name", () => {
    expect(modelDisplayName("claude-sonnet-5", models)).toBe("Claude Sonnet 5");
  });

  it("falls back to the raw ID for an unknown model", () => {
    expect(modelDisplayName("claude-haiku-1", models)).toBe("claude-haiku-1");
  });

  it("returns undefined for undefined input", () => {
    expect(modelDisplayName(undefined, models)).toBeUndefined();
  });

  it("falls back to the raw ID when the models list is empty", () => {
    expect(modelDisplayName("claude-sonnet-5", [])).toBe("claude-sonnet-5");
  });
});
