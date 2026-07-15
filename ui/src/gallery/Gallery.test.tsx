import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { Gallery } from "./Gallery";

const COMPONENT_NAMES = [
  "button",
  "chip",
  "card",
  "panel",
  "sidesheet",
  "toast",
  "resizablepanel",
  "iconrail",
  "draft",
];

const COMBOS = [
  "clay-dark",
  "clay-light",
  "space-dark",
  "space-light",
  "glacier-dark",
  "glacier-light",
];

describe("Gallery", () => {
  it("renders without throwing", () => {
    expect(() => render(<Gallery />)).not.toThrow();
  });

  it("renders all 6 palette×theme sections", () => {
    render(<Gallery />);
    for (const combo of COMBOS) {
      expect(screen.getByTestId(`combo-${combo}`)).toBeTruthy();
    }
  });

  it.each(COMPONENT_NAMES)("renders the %s group in all 6 sections", (name) => {
    render(<Gallery />);
    for (const combo of COMBOS) {
      const section = screen.getByTestId(`combo-${combo}`);
      expect(section.querySelector(`[data-testid="group-${name}"]`)).toBeTruthy();
    }
  });

  it("clicking a palette tab highlights only that palette's tab and dims other sections", () => {
    render(<Gallery />);
    const spaceTab = screen.getByRole("tab", { name: "space" });
    fireEvent.click(spaceTab);
    expect(spaceTab.getAttribute("aria-selected")).toBe("true");
    expect(
      screen.getByRole("tab", { name: "clay" }).getAttribute("aria-selected"),
    ).toBe("false");
    expect(screen.getByTestId("combo-clay-dark").className).toMatch(/dimmed/);
    expect(screen.getByTestId("combo-space-dark").className).not.toMatch(/dimmed/);
  });

  it("clicking the theme toggle cycles and dims non-matching sections", () => {
    render(<Gallery />);
    const themeToggle = screen.getByText(/^Theme:/);
    expect(themeToggle.textContent).toBe("Theme: all");

    fireEvent.click(themeToggle);
    expect(themeToggle.textContent).toBe("Theme: dark");
    expect(screen.getByTestId("combo-clay-light").className).toMatch(/dimmed/);
    expect(screen.getByTestId("combo-clay-dark").className).not.toMatch(/dimmed/);

    fireEvent.click(themeToggle);
    expect(themeToggle.textContent).toBe("Theme: light");

    fireEvent.click(themeToggle);
    expect(themeToggle.textContent).toBe("Theme: all");
  });
});
