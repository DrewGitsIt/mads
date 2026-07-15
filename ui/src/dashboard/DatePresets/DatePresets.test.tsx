import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { DatePresets } from "./DatePresets";

describe("DatePresets", () => {
  it("renders all four presets with the current value pressed", () => {
    render(<DatePresets value="30d" onChange={() => {}} />);
    expect(screen.getByRole("button", { name: "7d" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "30d" }).getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByRole("button", { name: "90d" }).getAttribute("aria-pressed")).toBe("false");
    expect(screen.getByRole("button", { name: "All" })).toBeTruthy();
  });

  it("calls onChange with the clicked preset's id", () => {
    let selected: string | undefined;
    render(<DatePresets value="7d" onChange={(p) => (selected = p)} />);
    fireEvent.click(screen.getByRole("button", { name: "90d" }));
    expect(selected).toBe("90d");
  });
});
