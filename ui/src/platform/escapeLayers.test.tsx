import { render, fireEvent } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { useEscapeLayer } from "./escapeLayers";

function Layer({ active = true, onEscape }: { active?: boolean; onEscape: () => void }) {
  useEscapeLayer(active, onEscape);
  return null;
}

describe("useEscapeLayer", () => {
  it("gives Escape to the most recently opened layer only", () => {
    const lens = vi.fn();
    const sheet = vi.fn();
    const { rerender } = render(
      <>
        <Layer onEscape={lens} />
        <Layer onEscape={sheet} />
      </>,
    );
    fireEvent.keyDown(document, { key: "Escape" });
    expect(sheet).toHaveBeenCalledTimes(1);
    expect(lens).not.toHaveBeenCalled();

    rerender(
      <>
        <Layer onEscape={lens} />
        <Layer active={false} onEscape={sheet} />
      </>,
    );
    fireEvent.keyDown(document, { key: "Escape" });
    expect(lens).toHaveBeenCalledTimes(1);
    expect(sheet).toHaveBeenCalledTimes(1);
  });

  it("stands aside while a kit Popover is open", () => {
    const lens = vi.fn();
    render(
      <>
        <Layer onEscape={lens} />
        <div data-popover-open="" />
      </>,
    );
    fireEvent.keyDown(document, { key: "Escape" });
    expect(lens).not.toHaveBeenCalled();
  });

  it("ignores a key another handler already claimed", () => {
    const lens = vi.fn();
    render(<Layer onEscape={lens} />);
    const e = new KeyboardEvent("keydown", { key: "Escape", cancelable: true });
    e.preventDefault();
    document.dispatchEvent(e);
    expect(lens).not.toHaveBeenCalled();
  });
});
