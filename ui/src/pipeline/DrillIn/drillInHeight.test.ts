import { afterEach, describe, expect, it } from "vitest";
import { loadDrillInHeight, saveDrillInHeight } from "./drillInHeight";

afterEach(() => {
  localStorage.clear();
});

describe("drillInHeight", () => {
  it("round-trips a height through localStorage", () => {
    saveDrillInHeight(420);
    expect(loadDrillInHeight()).toBe(420);
  });

  it("returns undefined when nothing is stored", () => {
    expect(loadDrillInHeight()).toBeUndefined();
  });

  it("falls back to undefined rather than throwing on corrupt JSON", () => {
    localStorage.setItem("mads.pipeline.drillInHeight", "{not valid json");
    expect(loadDrillInHeight()).toBeUndefined();
  });
});
