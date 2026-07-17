import { describe, expect, it } from "vitest";
import { computeHighlightSet } from "./highlight";
import type { GraphEdgeLayout } from "./layout";

function edge(from: string, to: string): GraphEdgeLayout {
  return { id: `${from}->${to}`, from, to, inCycle: false };
}

// Diamond: A -> B, A -> C, B -> D, C -> D.
const diamond: GraphEdgeLayout[] = [edge("a", "b"), edge("a", "c"), edge("b", "d"), edge("c", "d")];

// Disjoint second component, e -> f, sharing no ids with the diamond.
const edges = [...diamond, edge("e", "f")];

describe("computeHighlightSet", () => {
  it("selecting the root pulls in every descendant of the diamond", () => {
    expect(computeHighlightSet("a", edges)).toEqual(new Set(["a", "b", "c", "d"]));
  });

  it("selecting the sink pulls in every ancestor of the diamond", () => {
    expect(computeHighlightSet("d", edges)).toEqual(new Set(["a", "b", "c", "d"]));
  });

  it("selecting a middle node pulls in its ancestors and descendants but not the sibling branch", () => {
    expect(computeHighlightSet("b", edges)).toEqual(new Set(["a", "b", "d"]));
  });

  it("never pulls in a disjoint component's ids", () => {
    const set = computeHighlightSet("a", edges);
    expect(set.has("e")).toBe(false);
    expect(set.has("f")).toBe(false);
  });

  it("selecting a node in the disjoint component never touches the diamond", () => {
    const set = computeHighlightSet("e", edges);
    expect(set).toEqual(new Set(["e", "f"]));
  });

  it("does not infinite-loop on a cycle and includes every cycle participant", () => {
    const cyclic: GraphEdgeLayout[] = [edge("x", "y"), edge("y", "x")];
    expect(computeHighlightSet("x", cyclic)).toEqual(new Set(["x", "y"]));
  });
});
