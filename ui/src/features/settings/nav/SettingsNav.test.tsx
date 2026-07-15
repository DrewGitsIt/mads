import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { SettingsNav, SETTINGS_NAV_ITEMS } from "./SettingsNav";

describe("SettingsNav", () => {
  it("renders every section in MADS-UX §3.7's order", () => {
    render(<SettingsNav activeId="appearance" onSelect={() => {}} />);
    const rendered = screen.getAllByRole("button").map((b) => b.textContent);
    expect(rendered).toEqual(SETTINGS_NAV_ITEMS.map((i) => i.label));
  });

  it("marks the active section and calls onSelect on click", () => {
    const onSelect = vi.fn();
    render(<SettingsNav activeId="general" onSelect={onSelect} />);
    expect(screen.getByText("General").getAttribute("aria-current")).toBe("page");
    fireEvent.click(screen.getByText("Credentials"));
    expect(onSelect).toHaveBeenCalledWith("credentials");
  });
});
