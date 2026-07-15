import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { Popover } from "./Popover";

describe("Popover", () => {
  it("renders the anchor always, and children only when open", () => {
    const { rerender } = render(
      <Popover open={false} onClose={() => {}} anchor={<button>Open</button>}>
        <div>Content</div>
      </Popover>,
    );
    expect(screen.getByText("Open")).toBeTruthy();
    expect(screen.queryByText("Content")).toBeNull();

    rerender(
      <Popover open onClose={() => {}} anchor={<button>Open</button>}>
        <div>Content</div>
      </Popover>,
    );
    expect(screen.getByText("Content")).toBeTruthy();
  });

  it("calls onClose on outside click", () => {
    const onClose = vi.fn();
    render(
      <div>
        <div data-testid="outside">Outside</div>
        <Popover open onClose={onClose} anchor={<button>Open</button>}>
          <div>Content</div>
        </Popover>
      </div>,
    );
    fireEvent.mouseDown(screen.getByTestId("outside"));
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("does not call onClose on inside click", () => {
    const onClose = vi.fn();
    render(
      <Popover open onClose={onClose} anchor={<button>Open</button>}>
        <div>Content</div>
      </Popover>,
    );
    fireEvent.mouseDown(screen.getByText("Content"));
    expect(onClose).not.toHaveBeenCalled();
  });

  it("calls onClose on Escape", () => {
    const onClose = vi.fn();
    render(
      <Popover open onClose={onClose} anchor={<button>Open</button>}>
        <div>Content</div>
      </Popover>,
    );
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onClose).toHaveBeenCalledOnce();
  });
});
