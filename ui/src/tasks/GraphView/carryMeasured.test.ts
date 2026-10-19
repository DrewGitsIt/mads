import { describe, expect, it } from "vitest";
import type { Node } from "@xyflow/react";
import { carryMeasured } from "./carryMeasured";

const node = (id: string, extra: Partial<Node> = {}): Node => ({ id, position: { x: 0, y: 0 }, data: {}, ...extra });

describe("carryMeasured", () => {
  it("keeps a measured size across a resync that rebuilds the node objects", () => {
    const prev = [node("a", { measured: { width: 220, height: 86 } }), node("b")];
    const next = [node("a", { data: { fresh: true } }), node("b"), node("c")];
    const merged = carryMeasured(prev, next);
    expect(merged[0]).toEqual({ ...next[0], measured: { width: 220, height: 86 } });
    expect(merged[1]).toBe(next[1]);
    expect(merged[2]).toBe(next[2]);
  });

  it("prefers a size already on the fresh node", () => {
    const prev = [node("a", { measured: { width: 220, height: 86 } })];
    const next = [node("a", { measured: { width: 40, height: 40 } })];
    expect(carryMeasured(prev, next)[0].measured).toEqual({ width: 40, height: 40 });
  });
});
