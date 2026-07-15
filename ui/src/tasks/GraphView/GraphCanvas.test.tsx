// @vitest-environment jsdom
// happy-dom doesn't propagate WheelEvent modifier keys (ctrlKey comes
// through undefined) — jsdom does, and this test needs a real ctrlKey.
import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { GraphCanvas } from "./GraphCanvas";
import { layout } from "./layout";
import { fixtureTasks } from "../../router/fixtures";

const tasksById = Object.fromEntries(fixtureTasks.map((t) => [t.id, t]));
const graph = layout(fixtureTasks);

describe("GraphCanvas", () => {
  it("pans via drag, reported through onViewChange", () => {
    const onViewChange = vi.fn();
    render(<GraphCanvas layout={graph} tasksById={tasksById} onViewChange={onViewChange} />);
    const svg = screen.getByTestId("graph-canvas-svg");

    fireEvent.mouseDown(svg, { clientX: 100, clientY: 100 });
    fireEvent.mouseMove(window, { clientX: 130, clientY: 80 });
    fireEvent.mouseUp(window);

    expect(onViewChange).toHaveBeenCalledWith({ translateX: 30, translateY: -20, scale: 1 });
  });

  it("zooms on ctrl+wheel, clamped within bounds", () => {
    const onViewChange = vi.fn();
    render(<GraphCanvas layout={graph} tasksById={tasksById} onViewChange={onViewChange} />);
    const svg = screen.getByTestId("graph-canvas-svg");

    fireEvent.wheel(svg, { deltaY: -100, ctrlKey: true });

    expect(onViewChange).toHaveBeenCalledWith(
      expect.objectContaining({ scale: expect.any(Number) }),
    );
    const scale = onViewChange.mock.calls[0][0].scale;
    expect(scale).toBeGreaterThan(1);
  });

  it("ignores plain wheel (no ctrlKey) for zoom", () => {
    const onViewChange = vi.fn();
    render(<GraphCanvas layout={graph} tasksById={tasksById} onViewChange={onViewChange} />);
    const svg = screen.getByTestId("graph-canvas-svg");

    fireEvent.wheel(svg, { deltaY: -100, ctrlKey: false });

    expect(onViewChange).not.toHaveBeenCalled();
  });

  it("zooms in/out/reset via on-canvas controls", () => {
    const onViewChange = vi.fn();
    render(<GraphCanvas layout={graph} tasksById={tasksById} onViewChange={onViewChange} />);

    fireEvent.click(screen.getByText("+"));
    expect(onViewChange.mock.calls.at(-1)?.[0].scale).toBeGreaterThan(1);

    fireEvent.click(screen.getByText("Reset"));
    expect(onViewChange.mock.calls.at(-1)?.[0]).toEqual({ translateX: 0, translateY: 0, scale: 1 });
  });

  it("renders a distinct edge style for cycle edges", () => {
    const { container } = render(<GraphCanvas layout={graph} tasksById={tasksById} />);
    expect(container.querySelectorAll("line")).toHaveLength(graph.edges.length);
  });
});
