import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { RestartBadge } from "./RestartBadge";

describe("RestartBadge", () => {
  it("renders nothing when not visible", () => {
    const { container } = render(<RestartBadge visible={false} />);
    expect(container.firstChild).toBeNull();
  });

  it("renders the restart-required label when visible", () => {
    render(<RestartBadge visible={true} />);
    expect(screen.getByText(/restart required/i)).toBeTruthy();
  });
});
