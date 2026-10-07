import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { ContextRim } from "./ContextRim";
import type { Usage } from "../../router/types";

const at = (context_tokens: number, context_window?: number): Usage => ({
  input_tokens: 0,
  output_tokens: 0,
  context_tokens,
  context_window,
});

describe("ContextRim", () => {
  it("is a plain hairline with no label and no number without usage", () => {
    const { container } = render(<ContextRim contextWindow={200_000} />);
    expect(screen.queryByTestId("context-meter")).toBeNull();
    expect(screen.queryByTestId("context-label")).toBeNull();
    expect(container.textContent).toBe("");
    expect(container.querySelector('[data-rim="empty"]')).toBeTruthy();
  });

  it("labels tokens / window · % context, preferring the provider's window", () => {
    render(<ContextRim usage={at(106_496, 128_000)} contextWindow={200_000} />);
    expect(screen.getByTestId("context-label").textContent).toBe("106.5k / 128k · 83% context");
    const rim = screen.getByTestId("context-meter");
    expect(rim.getAttribute("aria-valuenow")).toBe("83");
  });

  it("fills to the occupancy", () => {
    render(<ContextRim usage={at(50_000)} contextWindow={200_000} />);
    const fill = screen.getByTestId("context-meter").querySelector("i") as HTMLElement;
    expect(fill.style.width).toBe("25%");
  });

  it("stays quiet and unpinned below 70%", () => {
    render(<ContextRim usage={at(138_000)} contextWindow={200_000} />);
    const rim = screen.getByTestId("context-meter");
    expect(rim.getAttribute("data-tone")).toBe("quiet");
    expect(rim.hasAttribute("data-pinned")).toBe(false);
  });

  it("turns warn and pins its label from 70%", () => {
    render(<ContextRim usage={at(140_000)} contextWindow={200_000} />);
    const rim = screen.getByTestId("context-meter");
    expect(rim.getAttribute("data-tone")).toBe("warn");
    expect(rim.hasAttribute("data-pinned")).toBe(true);
  });

  it("turns bad from 90%", () => {
    render(<ContextRim usage={at(180_000)} contextWindow={200_000} />);
    expect(screen.getByTestId("context-meter").getAttribute("data-tone")).toBe("bad");
  });

  it("clamps at 100%", () => {
    render(<ContextRim usage={at(400_000)} contextWindow={200_000} />);
    expect(screen.getByTestId("context-label").textContent).toContain("100% context");
  });

  it("offers Compact from 80% when wired, and not below", () => {
    const onCompact = vi.fn();
    const { rerender } = render(<ContextRim usage={at(150_000)} contextWindow={200_000} onCompact={onCompact} />);
    expect(screen.queryByRole("button", { name: "Compact" })).toBeNull();
    rerender(<ContextRim usage={at(160_000)} contextWindow={200_000} onCompact={onCompact} />);
    fireEvent.click(screen.getByRole("button", { name: "Compact" }));
    expect(onCompact).toHaveBeenCalled();
  });

  it("offers no Compact without a handler", () => {
    render(<ContextRim usage={at(190_000)} contextWindow={200_000} />);
    expect(screen.queryByRole("button", { name: "Compact" })).toBeNull();
  });
});
