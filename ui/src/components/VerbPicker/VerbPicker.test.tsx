import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { VerbPicker } from "./VerbPicker";

const OPTIONS = [
  { value: "none", label: "not use", description: "Nothing." },
  { value: "view", label: "view", description: "See sessions." },
  { value: "chat", label: "chat with", description: "Message.", disabled: true },
  { value: "control", label: "control", description: "Everything." },
];

describe("VerbPicker", () => {
  it("opens to options that explain themselves; picks one; skips disabled ones by keyboard", () => {
    const onChange = vi.fn();
    render(<VerbPicker value="view" options={OPTIONS} onChange={onChange} aria-label="What spark may do" />);
    const trigger = screen.getByRole("button", { name: "What spark may do" });
    expect(trigger.textContent).toBe("view");
    fireEvent.click(trigger);
    expect(screen.getByRole("option", { name: /chat with/ }).getAttribute("aria-disabled")).toBe("true");
    fireEvent.click(screen.getByRole("option", { name: /chat with/ }));
    expect(onChange).not.toHaveBeenCalled();
    fireEvent.keyDown(trigger, { key: "ArrowDown" });
    fireEvent.keyDown(trigger, { key: "Enter" });
    expect(onChange).toHaveBeenCalledWith("control");
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("Escape closes without a change", () => {
    const onChange = vi.fn();
    render(<VerbPicker value="view" options={OPTIONS} onChange={onChange} aria-label="x" />);
    fireEvent.click(screen.getByRole("button", { name: "x" }));
    fireEvent.keyDown(screen.getByRole("button", { name: "x" }), { key: "Escape" });
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(onChange).not.toHaveBeenCalled();
  });
});
