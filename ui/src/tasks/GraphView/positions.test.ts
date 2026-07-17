import { afterEach, describe, expect, it } from "vitest";
import { loadPositions, savePositions } from "./positions";

afterEach(() => {
  localStorage.clear();
});

describe("positions", () => {
  it("round-trips positions through localStorage", () => {
    savePositions("ses_1", { tsk_a: { x: 10, y: 20 } });
    expect(loadPositions("ses_1")).toEqual({ tsk_a: { x: 10, y: 20 } });
  });

  it("returns {} for a session with no stored positions", () => {
    expect(loadPositions("ses_missing")).toEqual({});
  });

  it("doesn't let two session ids collide", () => {
    savePositions("ses_1", { tsk_a: { x: 1, y: 1 } });
    savePositions("ses_2", { tsk_b: { x: 2, y: 2 } });
    expect(loadPositions("ses_1")).toEqual({ tsk_a: { x: 1, y: 1 } });
    expect(loadPositions("ses_2")).toEqual({ tsk_b: { x: 2, y: 2 } });
  });

  it("falls back to {} rather than throwing on corrupt JSON", () => {
    localStorage.setItem("mads.taskGraph.positions.ses_1", "{not valid json");
    expect(loadPositions("ses_1")).toEqual({});
  });
});
