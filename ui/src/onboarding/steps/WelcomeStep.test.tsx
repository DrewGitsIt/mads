import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { WelcomeStep } from "./WelcomeStep";

describe("WelcomeStep", () => {
  it("calls onNext when Get started is clicked", () => {
    const onNext = vi.fn();
    render(<WelcomeStep onNext={onNext} />);
    fireEvent.click(screen.getByText("Get started"));
    expect(onNext).toHaveBeenCalledTimes(1);
  });
});
