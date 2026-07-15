import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { QuadrantField } from "./QuadrantField";

describe("QuadrantField", () => {
  it("calls onChange with the selected quadrant", () => {
    const onChange = vi.fn();
    render(<QuadrantField onChange={onChange} />);
    fireEvent.change(screen.getByLabelText("Quadrant"), {
      target: { value: "investigator" },
    });
    expect(onChange).toHaveBeenCalledWith("investigator");
  });

  it("shows quadrant descriptions in the tooltip popover", () => {
    render(<QuadrantField onChange={vi.fn()} />);
    fireEvent.click(screen.getByLabelText("What is a quadrant?"));
    expect(screen.getByText(/High clarity, high agency/)).toBeTruthy();
  });
});
