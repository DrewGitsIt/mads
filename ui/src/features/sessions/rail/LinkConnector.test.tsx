import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { LINK_ANCHOR_Y, LinkConnector } from "./LinkConnector";

describe("LinkConnector", () => {
  it("anchors on the vertical centre of the name's first line", () => {
    // 1px border + 8px padding + half of 12.5px * 1.35 line-height, less the
    // 1px border (the SVG is placed from the padding box), plus the 7px the
    // box starts above the card.
    const centre = 1 + 8 + (12.5 * 1.35) / 2 - 1 + 7;
    expect(Math.abs(LINK_ANCHOR_Y - centre)).toBeLessThanOrEqual(0.25);
  });

  it("ends the line and both heads at that offset, on the card's border", () => {
    for (const watched of [false, true]) {
      const { container, unmount } = render(<LinkConnector watched={watched} />);
      const paths = [...container.querySelectorAll("path")].map((p) => p.getAttribute("d")!);
      expect(paths[0]).toBe(`M6 0 L6 10 Q6 ${LINK_ANCHOR_Y} 21 ${LINK_ANCHOR_Y}`);
      expect(paths[1]).toContain(`${LINK_ANCHOR_Y} `);
      unmount();
    }
  });
});
