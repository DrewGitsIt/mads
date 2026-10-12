import { describe, expect, it } from "vitest";
import { domMatches } from "./domMatches";
import { findPattern } from "./findText";

function el(html: string): HTMLElement {
  const div = document.createElement("div");
  div.innerHTML = html;
  return div;
}

const p = (query: string) => findPattern({ query, matchCase: false })!;

describe("domMatches", () => {
  it("finds every match as a range over the rendered text", () => {
    const ranges = domMatches(el("<p>a worktree per session</p><p>the worktree</p>"), p("worktree"));
    expect(ranges.map((r) => r.toString())).toEqual(["worktree", "worktree"]);
  });

  it("finds a match that spans formatting", () => {
    const [range] = domMatches(el("<p>work<strong>tree</strong>s</p>"), p("worktree"));
    expect(range.toString()).toBe("worktree");
    expect(range.startContainer.nodeValue).toBe("work");
    expect(range.endContainer.nodeValue).toBe("tree");
  });

  it("skips chrome: timestamps and anything marked data-find-skip", () => {
    const root = el(
      '<p>copy this</p><button data-find-skip>Copy</button><time>copy o\'clock</time><div data-find-skip><span>copy</span></div>',
    );
    expect(domMatches(root, p("copy")).map((r) => r.toString())).toEqual(["copy"]);
  });

  it("finds nothing in an empty element", () => {
    expect(domMatches(el(""), p("x"))).toEqual([]);
  });
});
