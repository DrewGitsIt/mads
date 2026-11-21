import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { InfoTip, Tooltip } from "./Tooltip";

describe("Tooltip", () => {
  it("describes its anchor, which can take focus", () => {
    render(<Tooltip tip="Not reached yet.">—</Tooltip>);
    const tip = screen.getByRole("tooltip");
    expect(tip.textContent).toBe("Not reached yet.");
    const anchor = tip.parentElement!;
    expect(anchor.getAttribute("aria-describedby")).toBe(tip.id);
    expect(anchor.tabIndex).toBe(0);
  });

  it("an InfoTip is a labelled (i) button described by its tip", () => {
    render(<InfoTip label="Highest session trust">Caps the trust.</InfoTip>);
    const button = screen.getByRole("button", { name: "About Highest session trust" });
    expect(document.getElementById(button.getAttribute("aria-describedby")!)!.textContent).toBe("Caps the trust.");
  });
});
