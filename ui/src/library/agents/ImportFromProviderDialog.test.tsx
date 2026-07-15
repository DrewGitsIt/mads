import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { ImportFromProviderDialog } from "./ImportFromProviderDialog";

describe("ImportFromProviderDialog", () => {
  it("renders a field mapping preview and the preview-only disclaimer", () => {
    render(<ImportFromProviderDialog onClose={vi.fn()} />);
    expect(screen.getByText("systemPrompt")).toBeTruthy();
    expect(screen.getByText("prompt")).toBeTruthy();
    expect(screen.getByText(/preview only/i)).toBeTruthy();
  });

  it("calls onClose when Close is clicked", () => {
    const onClose = vi.fn();
    render(<ImportFromProviderDialog onClose={onClose} />);
    fireEvent.click(screen.getByText("Close"));
    expect(onClose).toHaveBeenCalled();
  });
});
