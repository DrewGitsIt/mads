import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { StatusGlyph } from "./StatusGlyph";
import { GRAPH_STATUS_ORDER } from "./graphStatus";

describe("StatusGlyph", () => {
  it.each(GRAPH_STATUS_ORDER)("renders a distinct, aria-hidden shape class for %s", (status) => {
    const { container } = render(<StatusGlyph status={status} />);
    const glyph = container.firstElementChild!;
    expect(glyph.getAttribute("aria-hidden")).toBe("true");
    expect(glyph.className).toMatch(new RegExp(status));
  });

  it("spins only when in_progress and animate is true", () => {
    const { container } = render(<StatusGlyph status="in_progress" animate />);
    expect(container.firstElementChild!.className).toMatch(/spin/);
  });

  it("does not spin when animate is false, even for in_progress", () => {
    const { container } = render(<StatusGlyph status="in_progress" animate={false} />);
    expect(container.firstElementChild!.className).not.toMatch(/spin/);
  });

  it("never spins for a non in_progress status even if animate is true", () => {
    const { container } = render(<StatusGlyph status="done" animate />);
    expect(container.firstElementChild!.className).not.toMatch(/spin/);
  });
});
