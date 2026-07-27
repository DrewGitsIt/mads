import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { ContextMeter } from "./ContextMeter";

describe("ContextMeter", () => {
  it("renders nothing when contextWindow is missing", () => {
    const { container } = render(<ContextMeter usage={{ input_tokens: 1000, output_tokens: 0 }} />);
    expect(container.innerHTML).toBe("");
  });

  it("renders nothing when usage is missing", () => {
    const { container } = render(<ContextMeter contextWindow={200000} />);
    expect(container.innerHTML).toBe("");
  });

  it("renders a percentage from input_tokens when both are provided", () => {
    render(<ContextMeter usage={{ input_tokens: 100000, output_tokens: 0 }} contextWindow={200000} />);
    expect(screen.getByText("50%")).toBeTruthy();
  });

  it("caps the percentage at 100 when usage exceeds the window", () => {
    render(<ContextMeter usage={{ input_tokens: 500000, output_tokens: 0 }} contextWindow={200000} />);
    expect(screen.getByText("100%")).toBeTruthy();
  });

  it("counts cache tokens toward occupancy alongside input_tokens", () => {
    render(
      <ContextMeter
        usage={{ input_tokens: 10000, output_tokens: 0, cache_read_input_tokens: 60000, cache_creation_input_tokens: 30000 }}
        contextWindow={200000}
      />,
    );
    expect(screen.getByText("50%")).toBeTruthy();
  });

  it("excludes output_tokens from occupancy (regression: previously summed input+output+cache)", () => {
    // Old math: (100000 + 100000) / 200000 = 100%. Correct math: prompt side
    // only (input_tokens, excluding output_tokens) = 100000 / 200000 = 50%.
    render(<ContextMeter usage={{ input_tokens: 100000, output_tokens: 100000 }} contextWindow={200000} />);
    expect(screen.getByText("50%")).toBeTruthy();
    expect(screen.queryByText("100%")).toBeNull();
  });
});
