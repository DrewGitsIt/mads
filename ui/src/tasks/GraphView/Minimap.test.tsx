import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { Minimap } from "./Minimap";
import { layout } from "./layout";
import { fixtureTasks } from "../../router/fixtures";

const graph = layout(fixtureTasks);

describe("Minimap", () => {
  it("renders one node marker per graph node", () => {
    const { container } = render(
      <Minimap
        layout={graph}
        view={{ translateX: 0, translateY: 0, scale: 1 }}
        viewportSize={{ width: 800, height: 600 }}
      />,
    );
    expect(container.querySelectorAll("circle")).toHaveLength(graph.nodes.length);
  });

  it("renders a viewport rect derived from view state, not DOM measurement", () => {
    const { container } = render(
      <Minimap
        layout={graph}
        view={{ translateX: 0, translateY: 0, scale: 1 }}
        viewportSize={{ width: 800, height: 600 }}
      />,
    );
    const rect = container.querySelector("rect")!;
    expect(Number(rect.getAttribute("width"))).toBeGreaterThan(0);
    expect(Number(rect.getAttribute("height"))).toBeGreaterThan(0);
  });

  it("invokes onRecenter with a view centered on the clicked point", () => {
    const onRecenter = vi.fn();
    render(
      <Minimap
        layout={graph}
        view={{ translateX: 0, translateY: 0, scale: 1 }}
        viewportSize={{ width: 800, height: 600 }}
        onRecenter={onRecenter}
      />,
    );
    fireEvent.click(screen.getByTestId("minimap-svg"), { clientX: 40, clientY: 30 });
    expect(onRecenter).toHaveBeenCalledTimes(1);
    const arg = onRecenter.mock.calls[0][0];
    expect(arg.scale).toBe(1);
    expect(typeof arg.translateX).toBe("number");
    expect(typeof arg.translateY).toBe("number");
  });
});
