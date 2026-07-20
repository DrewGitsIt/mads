import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { SessionActionsMenu } from "./SessionActionsMenu";

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
    expect(screen.getByRole("button", { name: /^Resume/ }).closest("ul")).not.toBe(
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
    expect(screen.getByRole("button", { name: /^Resume/ })).toBeTruthy();
  });
});
