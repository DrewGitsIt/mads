import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { HarnessMark } from "./HarnessMark";

describe("HarnessMark", () => {
  it("draws a known harness's mark, labelled with its registry name", () => {
    for (const provider of ["claude-code", "goose", "hermes"]) {
      const { unmount } = render(<HarnessMark provider={provider} />);
      const mark = screen.getByRole("img", { name: provider });
      expect(mark.querySelector("svg")).toBeTruthy();
      unmount();
    }
  });

  it("falls back to the first letter for a harness with no mark", () => {
    render(<HarnessMark provider="kiro-cli" />);
    expect(screen.getByRole("img", { name: "kiro-cli" }).textContent).toBe("K");
  });

  it("renders nothing without a provider", () => {
    const { container } = render(<HarnessMark />);
    expect(container.firstChild).toBeNull();
  });
});
