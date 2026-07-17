import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { TrustPill } from "./TrustPill";
import { TRUST_LEVELS } from "./trustLevels";

describe("TrustPill", () => {
  it("shows the resolved default trust level when unset", () => {
    render(<TrustPill resolved={{ value: "limited", source: "config" }} onChange={() => {}} />);
    expect(screen.getByText(/Limited \(default\)/)).toBeTruthy();
  });

  it("shows the plain placeholder when nothing resolves", () => {
    render(<TrustPill onChange={() => {}} />);
    expect(screen.getByText("Select trust")).toBeTruthy();
  });

  it("reports the selected trust level", () => {
    const onChange = vi.fn();
    render(<TrustPill resolved={{ value: "limited", source: "config" }} onChange={onChange} />);
    fireEvent.click(screen.getByText(/Limited \(default\)/));
    fireEvent.click(screen.getByText("Full access"));
    expect(onChange).toHaveBeenCalledWith("full");
  });

  it("renders an accurate one-line description for every trust option", () => {
    render(<TrustPill resolved={{ value: "limited", source: "config" }} onChange={() => {}} />);
    fireEvent.click(screen.getByText(/Limited \(default\)/));

    for (const level of TRUST_LEVELS) {
      expect(screen.getByText(level.description)).toBeTruthy();
    }
  });
});
