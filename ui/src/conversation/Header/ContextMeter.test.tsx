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

  it("prefers context_tokens over the summed aggregate (regression: claude-code sums cache reads per iteration, pegging the meter)", () => {
    // A 10-round-trip turn on a ~40k conversation: the billing aggregate
    // reads ~400k (meter would peg at 100%); the final API call's real
    // occupancy is 40k → 20%.
    render(
      <ContextMeter
        usage={{ input_tokens: 500, output_tokens: 0, cache_read_input_tokens: 400000, context_tokens: 40000 }}
        contextWindow={200000}
      />,
    );
    expect(screen.getByText("20%")).toBeTruthy();
  });

  it("prefers the provider-reported context_window over the static prop", () => {
    // A local 128k model judged against the 200k table would read 50%; the
    // runtime-reported window says 78%.
    render(
      <ContextMeter
        usage={{ input_tokens: 100000, output_tokens: 0, context_window: 128000 }}
        contextWindow={200000}
      />,
    );
    expect(screen.getByText("78%")).toBeTruthy();
  });

  it("renders from usage.context_window alone, without the static prop", () => {
    render(<ContextMeter usage={{ input_tokens: 64000, output_tokens: 0, context_window: 128000 }} />);
    expect(screen.getByText("50%")).toBeTruthy();
  });
});
