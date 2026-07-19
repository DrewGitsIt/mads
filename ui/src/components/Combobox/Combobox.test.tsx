import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { Combobox } from "./Combobox";

const OPTIONS = ["claude-sonnet-5", "claude-haiku-4-5-20251001"];

describe("Combobox", () => {
  it("does not render suggestions until focused", () => {
    render(<Combobox value="" options={OPTIONS} onChange={vi.fn()} />);
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("shows every option on focus when empty", () => {
    render(<Combobox value="" options={OPTIONS} onChange={vi.fn()} />);
    fireEvent.focus(screen.getByRole("combobox"));
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(OPTIONS);
  });

  it("filters suggestions as the user types", () => {
    render(<Combobox value="haiku" options={OPTIONS} onChange={vi.fn()} />);
    fireEvent.focus(screen.getByRole("combobox"));
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
      "claude-haiku-4-5-20251001",
    ]);
  });

  it("still accepts free text with no matching option", () => {
    const onChange = vi.fn();
    render(<Combobox value="" options={OPTIONS} onChange={onChange} />);
    fireEvent.change(screen.getByRole("combobox"), { target: { value: "some-unknown-model" } });
    expect(onChange).toHaveBeenCalledWith("some-unknown-model");
  });

  it("selects the highlighted option on Enter", () => {
    const onChange = vi.fn();
    render(<Combobox value="" options={OPTIONS} onChange={onChange} />);
    const input = screen.getByRole("combobox");
    fireEvent.focus(input);
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onChange).toHaveBeenCalledWith("claude-sonnet-5");
  });

  it("navigates with the keyboard and closes on Escape", () => {
    render(<Combobox value="" options={OPTIONS} onChange={vi.fn()} />);
    const input = screen.getByRole("combobox");
    fireEvent.focus(input);
    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "Escape" });
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("clicking a suggestion fills the input and closes the list", () => {
    const onChange = vi.fn();
    render(<Combobox value="" options={OPTIONS} onChange={onChange} />);
    fireEvent.focus(screen.getByRole("combobox"));
    fireEvent.click(screen.getByText("claude-haiku-4-5-20251001"));
    expect(onChange).toHaveBeenCalledWith("claude-haiku-4-5-20251001");
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("shows the inherited value as placeholder when blank", () => {
    render(<Combobox value="" options={OPTIONS} onChange={vi.fn()} placeholder="sonnet (inherited)" />);
    expect((screen.getByRole("combobox") as HTMLInputElement).placeholder).toBe("sonnet (inherited)");
  });
});
