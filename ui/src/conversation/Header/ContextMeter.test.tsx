import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { ContextMeter } from "./ContextMeter";

describe("ContextMeter", () => {
  it("renders nothing when contextWindow is missing", () => {
    const { container } = render(<ContextMeter usedTokens={1000} />);
    expect(container.innerHTML).toBe("");
  });

  it("renders nothing when usedTokens is missing", () => {
    const { container } = render(<ContextMeter contextWindow={200000} />);
    expect(container.innerHTML).toBe("");
  });

  it("renders a percentage when both are provided", () => {
    render(<ContextMeter usedTokens={100000} contextWindow={200000} />);
    expect(screen.getByText("50%")).toBeTruthy();
  });

  it("caps the percentage at 100 when usage exceeds the window", () => {
    render(<ContextMeter usedTokens={500000} contextWindow={200000} />);
    expect(screen.getByText("100%")).toBeTruthy();
  });
});
