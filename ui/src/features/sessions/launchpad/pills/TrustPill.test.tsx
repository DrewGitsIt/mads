import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { TrustPill } from "./TrustPill";

describe("TrustPill", () => {
  it("defaults to a prompt-each-time label when unset", () => {
    render(<TrustPill onChange={() => {}} />);
    expect(screen.getByText(/Prompt each time/)).toBeTruthy();
  });

  it("reports the selected trust level", () => {
    const onChange = vi.fn();
    render(<TrustPill onChange={onChange} />);
    fireEvent.click(screen.getByText(/Prompt each time/));
    fireEvent.click(screen.getByText("Full access"));
    expect(onChange).toHaveBeenCalledWith("full");
  });
});
