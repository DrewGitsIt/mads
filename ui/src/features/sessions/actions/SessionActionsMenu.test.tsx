import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { SessionActionsMenu, shellMenuState } from "./SessionActionsMenu";

function renderMenu(props: Partial<Parameters<typeof SessionActionsMenu>[0]> = {}) {
  return render(
    <SessionActionsMenu
      sessionId="ses_abc123"
      activity="inactive"
      onClose={vi.fn()}
      onResume={vi.fn()}
      onDeleteRequest={vi.fn()}
      {...props}
    />,
  );
}

describe("SessionActionsMenu", () => {
  let writeText: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the full session id as a muted mono footer row, below the primary actions", () => {
    renderMenu();

    const idRow = screen.getByText("ses_abc123");
    expect(idRow.tagName).toBe("BUTTON");
    // Not grouped with the lifecycle actions list.
    expect(screen.getByRole("button", { name: /^Wake now/ }).closest("ul")).not.toBe(
      idRow.closest("ul"),
    );
  });

  it("copies the session id to the clipboard and shows a brief confirmation without dismissing the menu", async () => {
    const onClose = vi.fn();
    renderMenu({ onClose });

    fireEvent.click(screen.getByText("ses_abc123"));

    expect(writeText).toHaveBeenCalledWith("ses_abc123");
    expect(await screen.findByText("Copied")).toBeTruthy();
    expect(onClose).not.toHaveBeenCalled();
    // The menu itself stays mounted — no unmount/close side effect from the copy.
    expect(screen.getByRole("button", { name: /^Wake now/ })).toBeTruthy();
  });

  describe("shell item", () => {
    it("reads Open shell when the session has no live shell", () => {
      renderMenu({ onToggleShell: vi.fn(), shellState: "none" });
      expect(screen.getByRole("button", { name: "Open shell" })).toBeTruthy();
      expect(screen.queryByRole("img", { name: "shell is running" })).toBeNull();
    });

    it("reads Show shell with a dot when the shell lives but its dock is hidden", () => {
      renderMenu({ onToggleShell: vi.fn(), shellState: "hidden" });
      const item = screen.getByRole("button", { name: /Show shell/ });
      expect(item.querySelector('[role="img"]')).toBeTruthy();
    });

    it("reads Hide shell when the dock is showing", () => {
      renderMenu({ onToggleShell: vi.fn(), shellState: "visible" });
      expect(screen.getByRole("button", { name: "Hide shell" })).toBeTruthy();
    });

    it("derives the state from dock visibility and shell liveness", () => {
      expect(shellMenuState(true, true)).toBe("visible");
      expect(shellMenuState(true, false)).toBe("visible"); // an exited shell's dock is still showing
      expect(shellMenuState(false, true)).toBe("hidden");
      expect(shellMenuState(false, false)).toBe("none");
    });
  });
});
