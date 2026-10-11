import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { WorkspacesSection } from "./WorkspacesSection";
import { OPEN_LIBRARY_EVENT } from "../../../appNavigation";

describe("WorkspacesSection", () => {
  const listener = vi.fn();
  afterEach(() => {
    window.removeEventListener(OPEN_LIBRARY_EVENT, listener);
    listener.mockReset();
  });

  it("explains workspaces and opens the Library's workspaces shelf", () => {
    window.addEventListener(OPEN_LIBRARY_EVENT, listener);
    render(<WorkspacesSection />);
    expect(screen.getByText(/begins in its primary repo/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Open workspaces in the Library →" }));
    expect(listener).toHaveBeenCalledTimes(1);
    expect((listener.mock.calls[0][0] as CustomEvent).detail).toEqual({ shelf: "workspaces" });
  });
});
