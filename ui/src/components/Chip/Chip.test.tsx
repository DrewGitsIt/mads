import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { Chip } from "./Chip";

describe("Chip", () => {
  it("renders its label", () => {
    render(<Chip>Neutral</Chip>);
    expect(screen.getByText("Neutral")).toBeTruthy();
  });

  it("defaults to the neutral tone", () => {
    render(<Chip>Neutral</Chip>);
    expect(screen.getByText("Neutral").className).toMatch(/neutral/);
  });

  it.each(["ok", "warn", "bad", "info", "draft"] as const)(
    "applies the %s tone class",
    (tone) => {
      render(<Chip tone={tone}>{tone}</Chip>);
      expect(screen.getByText(tone).className).toMatch(new RegExp(tone));
    },
  );

  it("renders as a button with aria-pressed when interactive", () => {
    render(
      <Chip onClick={() => {}} pressed>
        Pressed
      </Chip>,
    );
    const el = screen.getByText("Pressed");
    expect(el.tagName).toBe("BUTTON");
    expect(el.getAttribute("aria-pressed")).toBe("true");
  });

  it("fires the click handler on a non-pressed interactive chip", () => {
    const onClick = vi.fn();
    render(
      <Chip onClick={onClick} pressed={false}>
        Toggle
      </Chip>,
    );
    fireEvent.click(screen.getByText("Toggle"));
    expect(onClick).toHaveBeenCalledTimes(1);
  });
});
