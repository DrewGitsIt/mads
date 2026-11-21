import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { Dialog } from "./Dialog";

describe("Dialog", () => {
  it("is named by its title, focuses the marked control, and Escape or the backdrop dismisses", () => {
    const onDismiss = vi.fn();
    render(
      <Dialog title="Pair a device" onDismiss={onDismiss} actions={<button type="button">Cancel</button>}>
        <button type="button" data-autofocus="">
          Copy
        </button>
      </Dialog>,
    );
    const dialog = screen.getByRole("dialog", { name: "Pair a device" });
    expect(document.activeElement?.textContent).toBe("Copy");
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onDismiss).toHaveBeenCalledTimes(1);
    fireEvent.mouseDown(dialog.parentElement!);
    expect(onDismiss).toHaveBeenCalledTimes(2);
    fireEvent.mouseDown(dialog);
    expect(onDismiss).toHaveBeenCalledTimes(2);
  });

  it("an alert is an alertdialog", () => {
    render(<Dialog alert title="Remove spark?" onDismiss={() => {}} actions={null} />);
    expect(screen.getByRole("alertdialog", { name: "Remove spark?" })).toBeTruthy();
  });
});
