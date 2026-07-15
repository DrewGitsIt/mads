import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { ObservationsInput } from "./ObservationsInput";

describe("ObservationsInput", () => {
  it("renders the current value and reports changes", () => {
    const onChange = vi.fn();
    render(<ObservationsInput value="hello" onChange={onChange} />);
    const textarea = screen.getByLabelText("Observations") as HTMLTextAreaElement;
    expect(textarea.value).toBe("hello");

    fireEvent.change(textarea, { target: { value: "hello world" } });
    expect(onChange).toHaveBeenCalledWith("hello world");
  });

  it("disables the textarea when disabled is set", () => {
    render(<ObservationsInput value="" onChange={() => {}} disabled />);
    expect((screen.getByLabelText("Observations") as HTMLTextAreaElement).disabled).toBe(true);
  });
});
