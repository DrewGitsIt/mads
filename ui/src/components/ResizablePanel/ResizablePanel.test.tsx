import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { ResizablePanel } from "./ResizablePanel";

describe("ResizablePanel", () => {
  it("starts at defaultSize", () => {
    render(
      <ResizablePanel defaultSize={200} minSize={100} maxSize={400}>
        content
      </ResizablePanel>,
    );
    const panel = screen.getByTestId("resize-handle").parentElement as HTMLElement;
    expect(panel.style.height).toBe("200px");
  });

  it("grows when the handle is dragged up (vertical)", () => {
    render(
      <ResizablePanel defaultSize={200} minSize={100} maxSize={400}>
        content
      </ResizablePanel>,
    );
    const handle = screen.getByTestId("resize-handle");
    const panel = handle.parentElement as HTMLElement;

    fireEvent.mouseDown(handle, { clientY: 500 });
    fireEvent.mouseMove(window, { clientY: 450 });
    fireEvent.mouseUp(window);

    expect(panel.style.height).toBe("250px");
  });

  it("clamps to maxSize", () => {
    render(
      <ResizablePanel defaultSize={200} minSize={100} maxSize={400}>
        content
      </ResizablePanel>,
    );
    const handle = screen.getByTestId("resize-handle");
    const panel = handle.parentElement as HTMLElement;

    fireEvent.mouseDown(handle, { clientY: 500 });
    fireEvent.mouseMove(window, { clientY: -5000 });
    fireEvent.mouseUp(window);

    expect(panel.style.height).toBe("400px");
  });

  it("clamps to minSize", () => {
    render(
      <ResizablePanel defaultSize={200} minSize={100} maxSize={400}>
        content
      </ResizablePanel>,
    );
    const handle = screen.getByTestId("resize-handle");
    const panel = handle.parentElement as HTMLElement;

    fireEvent.mouseDown(handle, { clientY: 500 });
    fireEvent.mouseMove(window, { clientY: 5000 });
    fireEvent.mouseUp(window);

    expect(panel.style.height).toBe("100px");
  });

  it("stops resizing after mouseup", () => {
    render(
      <ResizablePanel defaultSize={200} minSize={100} maxSize={400}>
        content
      </ResizablePanel>,
    );
    const handle = screen.getByTestId("resize-handle");
    const panel = handle.parentElement as HTMLElement;

    fireEvent.mouseDown(handle, { clientY: 500 });
    fireEvent.mouseMove(window, { clientY: 450 });
    fireEvent.mouseUp(window);
    fireEvent.mouseMove(window, { clientY: 0 });

    expect(panel.style.height).toBe("250px");
  });
});
