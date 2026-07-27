import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { ChipListField } from "./ChipListField";

describe("ChipListField", () => {
  it("renders existing values as chips", () => {
    render(<ChipListField label="Tags" value={["billing", "urgent"]} onChange={vi.fn()} />);
    expect(screen.getByText("billing ×")).toBeTruthy();
    expect(screen.getByText("urgent ×")).toBeTruthy();
  });

  it("adding a value calls onChange with the appended list", () => {
    const onChange = vi.fn();
    render(<ChipListField label="Tags" value={["billing"]} onChange={onChange} placeholder="e.g. billing" />);

    fireEvent.change(screen.getByPlaceholderText("e.g. billing"), { target: { value: "urgent" } });
    fireEvent.click(screen.getByText("Add"));

    expect(onChange).toHaveBeenCalledWith(["billing", "urgent"]);
  });

  it("clicking a chip removes it", () => {
    const onChange = vi.fn();
    render(<ChipListField label="Tags" value={["billing", "urgent"]} onChange={onChange} />);

    fireEvent.click(screen.getByText("billing ×"));

    expect(onChange).toHaveBeenCalledWith(["urgent"]);
  });

  it("does not add duplicate or empty values", () => {
    const onChange = vi.fn();
    render(<ChipListField label="Tags" value={["billing"]} onChange={onChange} placeholder="e.g. billing" />);

    fireEvent.change(screen.getByPlaceholderText("e.g. billing"), { target: { value: "billing" } });
    fireEvent.click(screen.getByText("Add"));

    expect(onChange).not.toHaveBeenCalled();
  });
});
